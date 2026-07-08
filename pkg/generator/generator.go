package generator

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/controller-tools/pkg/deepcopy"
	"sigs.k8s.io/controller-tools/pkg/genall"
	"sigs.k8s.io/controller-tools/pkg/loader"
	"sigs.k8s.io/controller-tools/pkg/markers"
)

type Generator struct {
	inputDir     string
	outputDir    string
	fset         *token.FileSet
	publicTypes  map[string]bool
	referencedTypes map[string]bool
}

func NewGenerator(inputDir, outputDir string) *Generator {
	return &Generator{
		inputDir:        inputDir,
		outputDir:       outputDir,
		fset:            token.NewFileSet(),
		publicTypes:     make(map[string]bool),
		referencedTypes: make(map[string]bool),
	}
}

func (g *Generator) Generate() error {
	// Create README in output directory
	if err := g.generateReadme(); err != nil {
		return fmt.Errorf("generating README: %w", err)
	}

	var files []string
	var aggregatorFiles []string

	err := filepath.Walk(g.inputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}

		// Check if this is an aggregator file (not in a versioned subdirectory)
		relPath, err := filepath.Rel(g.inputDir, path)
		if err != nil {
			return err
		}

		// If the file is directly under a package directory (not in vX subdir)
		// and not groupversion_info.go, it's likely an aggregator
		dir := filepath.Dir(relPath)
		if !strings.Contains(dir, string(filepath.Separator)+"v") && filepath.Base(path) != "groupversion_info.go" {
			aggregatorFiles = append(aggregatorFiles, path)
		} else {
			files = append(files, path)
		}

		return nil
	})
	if err != nil {
		return err
	}

	for _, path := range files {
		if err := g.analyzeFile(path); err != nil {
			return fmt.Errorf("analyzing %s: %w", path, err)
		}
	}

	for _, path := range files {
		relPath, err := filepath.Rel(g.inputDir, path)
		if err != nil {
			return err
		}

		outputPath := filepath.Join(g.outputDir, relPath)

		if err := g.processFile(path, outputPath); err != nil {
			return fmt.Errorf("processing %s: %w", path, err)
		}
	}

	// Process aggregator files after type files
	for _, path := range aggregatorFiles {
		relPath, err := filepath.Rel(g.inputDir, path)
		if err != nil {
			return err
		}

		outputPath := filepath.Join(g.outputDir, relPath)

		if err := g.processAggregatorFile(path, outputPath); err != nil {
			return fmt.Errorf("processing aggregator %s: %w", path, err)
		}
	}

	fmt.Println("Generating deepcopy methods for internal APIs...")
	if err := g.generateDeepCopy(g.inputDir); err != nil {
		return fmt.Errorf("generating deepcopy for internal: %w", err)
	}

	fmt.Println("Generating deepcopy methods for public APIs...")
	if err := g.generateDeepCopy(g.outputDir); err != nil {
		return fmt.Errorf("generating deepcopy for public: %w", err)
	}

	fmt.Println("Generating schemas for internal APIs...")
	if err := g.generateSchemas(g.inputDir); err != nil {
		return fmt.Errorf("generating schemas for internal: %w", err)
	}

	fmt.Println("Generating schemas for public APIs...")
	if err := g.generateSchemas(g.outputDir); err != nil {
		return fmt.Errorf("generating schemas for public: %w", err)
	}

	fmt.Println("Generating conversion functions...")
	if err := g.generateConversions(); err != nil {
		return fmt.Errorf("generating conversions: %w", err)
	}

	return nil
}

func (g *Generator) analyzeFile(inputPath string) error {
	file, err := parser.ParseFile(g.fset, inputPath, nil, parser.ParseComments)
	if err != nil {
		return err
	}

	typesWithPublicFields := make(map[string]bool)

	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}

		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}

			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}

			hasPublic := false
			for _, field := range structType.Fields.List {
				if g.hasPublicMarker(field) {
					hasPublic = true
					g.collectReferencedTypes(field.Type)
				}
			}

			if hasPublic {
				typesWithPublicFields[typeSpec.Name.Name] = true
			}
		}
	}

	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}

		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}

			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}

			if typesWithPublicFields[typeSpec.Name.Name] {
				g.publicTypes[typeSpec.Name.Name] = true
				continue
			}

			referencesPublicType := false
			for _, field := range structType.Fields.List {
				if g.fieldReferencesPublicType(field, typesWithPublicFields) {
					referencesPublicType = true
					break
				}
			}

			if referencesPublicType {
				g.publicTypes[typeSpec.Name.Name] = true
			}

			if baseName, ok := strings.CutSuffix(typeSpec.Name.Name, "List"); ok {
				if g.publicTypes[baseName] || typesWithPublicFields[baseName] {
					g.publicTypes[typeSpec.Name.Name] = true
				}
			}
		}
	}

	return nil
}

func (g *Generator) fieldReferencesPublicType(field *ast.Field, publicTypes map[string]bool) bool {
	return g.typeReferencesPublic(field.Type, publicTypes)
}

func (g *Generator) typeReferencesPublic(expr ast.Expr, publicTypes map[string]bool) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return publicTypes[t.Name]
	case *ast.StarExpr:
		return g.typeReferencesPublic(t.X, publicTypes)
	case *ast.ArrayType:
		return g.typeReferencesPublic(t.Elt, publicTypes)
	case *ast.MapType:
		return g.typeReferencesPublic(t.Key, publicTypes) || g.typeReferencesPublic(t.Value, publicTypes)
	}
	return false
}

func (g *Generator) hasPublicMarker(field *ast.Field) bool {
	if field.Doc == nil {
		return false
	}

	for _, comment := range field.Doc.List {
		if strings.Contains(comment.Text, "+orlop:public") {
			return true
		}
	}

	return false
}

func (g *Generator) collectReferencedTypes(expr ast.Expr) {
	switch t := expr.(type) {
	case *ast.Ident:
		g.referencedTypes[t.Name] = true
	case *ast.StarExpr:
		g.collectReferencedTypes(t.X)
	case *ast.ArrayType:
		g.collectReferencedTypes(t.Elt)
	case *ast.MapType:
		g.collectReferencedTypes(t.Key)
		g.collectReferencedTypes(t.Value)
	}
}

func (g *Generator) processFile(inputPath, outputPath string) error {
	file, err := parser.ParseFile(g.fset, inputPath, nil, parser.ParseComments)
	if err != nil {
		return err
	}

	baseName := filepath.Base(inputPath)
	if baseName == "groupversion_info.go" {
		if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
			return err
		}

		var buf bytes.Buffer
		if err := format.Node(&buf, g.fset, file); err != nil {
			return err
		}

		return os.WriteFile(outputPath, buf.Bytes(), 0644)
	}

	filteredFile := g.filterFile(file)
	if filteredFile == nil {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}

	var buf bytes.Buffer
	if err := format.Node(&buf, g.fset, filteredFile); err != nil {
		return err
	}

	return os.WriteFile(outputPath, buf.Bytes(), 0644)
}

func (g *Generator) processAggregatorFile(inputPath, outputPath string) error {
	file, err := parser.ParseFile(g.fset, inputPath, nil, parser.ParseComments)
	if err != nil {
		return err
	}

	// Rewrite imports to point to public API
	g.rewriteImports(file)

	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}

	var buf bytes.Buffer
	if err := format.Node(&buf, g.fset, file); err != nil {
		return err
	}

	// Add localSchemeBuilder by text manipulation (easier than AST manipulation for formatting)
	content := buf.String()
	content = strings.Replace(content,
		"var AddToSchemes runtime.SchemeBuilder",
		"var (\n\t// AddToSchemes may be used to add all resources defined in the project to a Scheme.\n\tAddToSchemes runtime.SchemeBuilder",
		1)

	// Close the var block and add localSchemeBuilder
	content = strings.Replace(content,
		"// AddToSchemes may be used to add all resources defined in the project to a Scheme.\n\tAddToSchemes runtime.SchemeBuilder = runtime.SchemeBuilder{",
		"AddToSchemes runtime.SchemeBuilder = runtime.SchemeBuilder{",
		1)
	content = strings.Replace(content,
		"\tv1.SchemeBuilder.AddToScheme,\n}",
		"\tv1.SchemeBuilder.AddToScheme,\n\t}\n\n\t// localSchemeBuilder is used for registration of conversion functions\n\tlocalSchemeBuilder = &AddToSchemes\n)",
		1)

	return os.WriteFile(outputPath, []byte(content), 0644)
}

func (g *Generator) rewriteImports(file *ast.File) {
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.IMPORT {
			continue
		}

		for _, spec := range genDecl.Specs {
			importSpec, ok := spec.(*ast.ImportSpec)
			if !ok {
				continue
			}

			// Get the import path without quotes
			importPath := strings.Trim(importSpec.Path.Value, "\"")

			// Replace /apis/internal/ with /apis/public/
			if strings.Contains(importPath, "/apis/internal/") {
				newPath := strings.Replace(importPath, "/apis/internal/", "/apis/public/", 1)
				importSpec.Path.Value = "\"" + newPath + "\""
			}
		}
	}
}


func (g *Generator) filterFile(file *ast.File) *ast.File {
	newFile := &ast.File{
		Name:    file.Name,
		Package: file.Package,
	}

	var imports []ast.Decl
	var typeDecls []ast.Decl
	var funcDecls []ast.Decl

	for _, decl := range file.Decls {
		// Handle function declarations (including init functions)
		if funcDecl, ok := decl.(*ast.FuncDecl); ok {
			if funcDecl.Name.Name == "init" {
				funcDecls = append(funcDecls, funcDecl)
			}
			continue
		}

		genDecl, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}

		if genDecl.Tok == token.IMPORT {
			imports = append(imports, decl)
			continue
		}

		if genDecl.Tok != token.TYPE {
			continue
		}

		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}

			if typeSpec.Doc == nil && genDecl.Doc != nil {
				typeSpec.Doc = genDecl.Doc
			}

			filteredSpec := g.filterTypeSpec(typeSpec)
			if filteredSpec != nil {
				// Create a new GenDecl for each type
				// Preserve TokPos to maintain proper formatting
				newGenDecl := &ast.GenDecl{
					Doc:    filteredSpec.Doc,
					TokPos: genDecl.TokPos,
					Tok:    genDecl.Tok,
					Specs:  []ast.Spec{filteredSpec},
				}
				// Move doc from TypeSpec to GenDecl for correct formatting
				filteredSpec.Doc = nil
				typeDecls = append(typeDecls, newGenDecl)
			}
		}
	}

	if len(typeDecls) == 0 {
		return nil
	}

	newFile.Imports = file.Imports
	newFile.Decls = append(imports, typeDecls...)
	newFile.Decls = append(newFile.Decls, funcDecls...)
	return newFile
}

func (g *Generator) filterTypeSpec(typeSpec *ast.TypeSpec) *ast.TypeSpec {
	structType, ok := typeSpec.Type.(*ast.StructType)
	if !ok {
		return typeSpec
	}

	typeName := typeSpec.Name.Name
	if !g.publicTypes[typeName] {
		return nil
	}

	newFields := &ast.FieldList{}

	for _, field := range structType.Fields.List {
		includeField, _ := g.shouldIncludeField(field)
		if includeField {
			newField := &ast.Field{
				Doc:     filterCommentsKeepNonOrlop(field.Doc),
				Names:   field.Names,
				Type:    g.filterType(field.Type),
				Tag:     field.Tag,
				Comment: field.Comment,
			}
			newFields.List = append(newFields.List, newField)
		}
	}

	return &ast.TypeSpec{
		Doc:  filterCommentsKeepNonOrlop(typeSpec.Doc),
		Name: typeSpec.Name,
		Type: &ast.StructType{
			Struct: structType.Struct,
			Fields: newFields,
		},
	}
}

func (g *Generator) filterType(expr ast.Expr) ast.Expr {
	switch t := expr.(type) {
	case *ast.Ident:
		return t
	case *ast.SelectorExpr:
		return t
	case *ast.StarExpr:
		return &ast.StarExpr{X: g.filterType(t.X)}
	case *ast.ArrayType:
		return &ast.ArrayType{
			Lbrack: t.Lbrack,
			Len:    t.Len,
			Elt:    g.filterType(t.Elt),
		}
	case *ast.MapType:
		return &ast.MapType{
			Map:   t.Map,
			Key:   g.filterType(t.Key),
			Value: g.filterType(t.Value),
		}
	default:
		return expr
	}
}

func (g *Generator) shouldIncludeField(field *ast.Field) (include bool, isPublic bool) {
	if len(field.Names) == 0 {
		return true, false
	}

	if field.Doc == nil {
		fieldType := g.getTypeName(field.Type)
		if g.publicTypes[fieldType] {
			return true, false
		}
		return false, false
	}

	for _, comment := range field.Doc.List {
		if strings.Contains(comment.Text, "+orlop:public") {
			return true, true
		}
	}

	fieldType := g.getTypeName(field.Type)
	if g.publicTypes[fieldType] {
		return true, false
	}

	return false, false
}

func (g *Generator) getTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return g.getTypeName(t.X)
	case *ast.ArrayType:
		return g.getTypeName(t.Elt)
	default:
		return ""
	}
}

func (g *Generator) generateDeepCopy(rootPath string) error {
	registry := &markers.Registry{}
	gen := deepcopy.Generator{
		HeaderFile: "hack/boilerplate.go.txt",
	}

	if err := gen.RegisterMarkers(registry); err != nil {
		return fmt.Errorf("failed to register markers: %w", err)
	}

	collector := &markers.Collector{Registry: registry}

	absPath, err := filepath.Abs(rootPath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	roots, err := loader.LoadRoots(absPath + "/...")
	if err != nil {
		return fmt.Errorf("failed to load packages: %w", err)
	}

	ctx := &genall.GenerationContext{
		Collector:  collector,
		Roots:      roots,
		Checker:    &loader.TypeChecker{},
		OutputRule: genall.OutputArtifacts{},
		InputRule:  genall.InputFromFileSystem,
	}

	if err := gen.Generate(ctx); err != nil {
		return fmt.Errorf("failed to generate deepcopy: %w", err)
	}

	return nil
}

func filterCommentsKeepNonOrlop(commentGroup *ast.CommentGroup) *ast.CommentGroup {
	if commentGroup == nil {
		return nil
	}

	var filtered []*ast.Comment
	for _, comment := range commentGroup.List {
		if !strings.Contains(comment.Text, "+orlop:public") {
			filtered = append(filtered, comment)
		}
	}

	if len(filtered) == 0 {
		return nil
	}

	return &ast.CommentGroup{List: filtered}
}

func (g *Generator) generateReadme() error {
	// Ensure output directory exists
	if err := os.MkdirAll(g.outputDir, 0755); err != nil {
		return err
	}

	readmeContent := `# Auto-Generated Public API

**WARNING: This directory is auto-generated. Do not modify files manually.**

This directory contains the public API types generated from the internal API definitions.
All files in this directory are created by the orlop-gen code generator based on the
source files in the internal API directory.

## Regenerating

To regenerate this directory, run:

` + "```bash" + `
make generate
# or
go run ./cmd/orlop-gen
` + "```" + `

Any manual changes will be lost when the generator is run again.

## Source

The source files are located in the internal API directory. To make changes:

1. Modify the internal API types
2. Add or update +orlop:public markers on fields that should be public
3. Run the code generator to update this directory

## Generated Files

This directory contains:
- Filtered API types based on +orlop:public markers
- Generated DeepCopy methods (zz_generated.deepcopy.go)
- Generated OpenAPI v3 schemas (zz_generated.schemas.go)
- Generated conversion functions (zz_generated.conversion.go)
- API version aggregator files with updated import paths
`

	readmePath := filepath.Join(g.outputDir, "README.md")
	return os.WriteFile(readmePath, []byte(readmeContent), 0644)
}
