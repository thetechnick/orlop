# Orlop - Public API Generator

Orlop is a code generator that creates filtered public API types from internal Kubernetes API definitions. It uses the `+orlop:public` marker comment to determine which fields should be included in the public API.

## Overview

The generator:
1. Reads Go source files from `apis/internal`
2. Filters types and fields based on `+orlop:public` markers
3. Generates cleaned types in `apis/public` maintaining the same directory structure
4. Preserves kubebuilder markers and other non-orlop comments
5. Copies `groupversion_info.go` files as-is
6. Copies `init()` functions that register types into the scheme
7. Copies API version aggregator files (e.g., `test.go`) with updated import paths
8. Generates DeepCopy methods for both internal and public APIs using controller-tools
9. Generates OpenAPI v3 schemas embedded in Go code for both internal and public APIs

## Usage

### Running the Generator

```bash
# Run directly
go run ./cmd/orlop-gen

# Or use the Makefile
make generate

# Clean generated files
make clean
```

### Command-line Options

```bash
go run ./cmd/orlop-gen -input-dir=apis/internal -output-dir=apis/public
```

## Marker Syntax

Mark fields you want to include in the public API with `+orlop:public`:

```go
type ObjectSpec struct {
    // +orlop:public
    PublicField   string `json:"publicField"`
    InternalField string `json:"internalField"`
    
    // +orlop:public
    Nested ObjectNested `json:"nested"`
}
```

## How It Works

### Field Filtering

1. **Direct marking**: Fields with `+orlop:public` are included
2. **Embedded fields**: Anonymous/embedded fields are always included
3. **Type propagation**: Types referenced by public fields are automatically included
4. **List types**: `*List` types for public types are automatically included

### Example

**Input** (`apis/internal/test/v1/object.go`):
```go
type Object struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    
    Spec   ObjectSpec   `json:"spec,omitempty"`
    Status ObjectStatus `json:"status,omitempty"`
}

type ObjectSpec struct {
    // +orlop:public
    PublicField   string `json:"publicField"`
    InternalField string `json:"internalField"`
}
```

**Output** (`apis/public/test/v1/object.go`):
```go
type Object struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    
    Spec   ObjectSpec   `json:"spec,omitempty"`
    Status ObjectStatus `json:"status,omitempty"`
}

type ObjectSpec struct {
    PublicField string `json:"publicField"`
}
```

## Generated Code

The generator produces several generated files:

### DeepCopy Methods (`zz_generated.deepcopy.go`)

Automatically generates DeepCopy, DeepCopyInto, and DeepCopyObject methods for all types in both the internal and public APIs. These methods are required for types that implement `runtime.Object` and are used by the Kubernetes API machinery.

### OpenAPI v3 Schemas (`zz_generated.schemas.go`)

Generates embedded OpenAPI v3 schemas for all CRD types. The schemas are:
- Embedded as YAML constants in Go code
- Pre-parsed into structural schema objects at init time
- Available as both raw YAML and parsed `schema.Structural` objects
- Generated for both internal and public APIs

The schemas include:
- `{TypeName}SchemaYAML` - Raw OpenAPI v3 schema as YAML
- `{TypeName}Schema` - Parsed structural schema
- `{TypeName}Plural` - Plural resource name

These schemas are used for server-side validation and pruning of unknown fields.

## Conversion Functions

Automatic conversion function generation using `k8s.io/code-generator/cmd/conversion-gen` is not currently supported because both internal and public APIs use the same package name (`v1`). The conversion-gen tool expects packages with different names (e.g., internal unversioned vs external versioned).

If conversion between internal and public types is needed, manual conversion functions can be written in non-generated files. These should follow the Kubernetes conversion patterns:

```go
// Convert_v1_Object_To_public_Object converts an internal Object to public Object
func Convert_internal_Object_To_public_Object(in *internal.Object, out *public.Object, s conversion.Scope) error {
    // Manual field-by-field conversion
    out.ObjectMeta = in.ObjectMeta
    // Only copy public fields
    out.Spec.PublicField = in.Spec.PublicField
    return nil
}
```

## Directory Structure

```
.
├── apis/
│   ├── internal/          # Internal API definitions with +orlop:public markers
│   │   └── test/
│   │       ├── test.go    # Version aggregator (SchemeBuilder)
│   │       └── v1/        # v1 API types
│   └── public/            # Generated public API (git-ignored recommended)
│       └── test/
│           ├── test.go    # Generated aggregator with public imports
│           └── v1/        # Filtered v1 API types
├── cmd/
│   └── orlop-gen/         # Generator CLI
├── pkg/
│   └── generator/         # Generator implementation
└── Makefile
```

## API Version Aggregator

The generator copies aggregator files (like `test.go`) that combine multiple API versions into a single `SchemeBuilder`. Import paths are automatically rewritten to point to the public API:

**Input** (`apis/internal/test/test.go`):
```go
import v1 "github.com/thetechnick/orlop/apis/internal/test/v1"
```

**Output** (`apis/public/test/test.go`):
```go
import v1 "github.com/thetechnick/orlop/apis/public/test/v1"
```

This allows consumers of the public API to register all types using `test.AddToScheme(scheme)`.

## License

Apache 2.0
