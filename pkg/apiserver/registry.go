package apiserver

import (
	"fmt"

	privatev1 "github.com/thetechnick/orlop/apis/private/test/v1"
	publicv1 "github.com/thetechnick/orlop/apis/public/test/v1"
	"github.com/thetechnick/orlop/pkg/apiserver/conversion"
	"github.com/thetechnick/orlop/pkg/apiserver/handlers"
	pkgschema "github.com/thetechnick/orlop/pkg/apiserver/schema"
	"github.com/thetechnick/orlop/pkg/apiserver/storage"
	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/runtime"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// ResourceInfo describes a single API resource type.
type ResourceInfo struct {
	// GVK is the GroupVersionKind for this resource
	GVK runtimeschema.GroupVersionKind
	// Plural is the plural name for the resource (e.g., "objects")
	Plural string
	// SchemaYAML is the OpenAPI v3 schema in YAML format
	SchemaYAML string
	// NewObjectFunc creates a new instance of the resource
	NewObjectFunc func() runtime.Object
	// NewListFunc creates a new list instance
	NewListFunc func() runtime.Object
	// PrivateNewFunc creates a new instance of the private resource (for converting handlers)
	PrivateNewFunc func() runtime.Object
}

// ResourceRegistry manages API resource registrations.
type ResourceRegistry struct {
	resources []ResourceInfo
}

// NewResourceRegistry creates a new resource registry.
func NewResourceRegistry() *ResourceRegistry {
	return &ResourceRegistry{
		resources: []ResourceInfo{},
	}
}

// Register adds a resource to the registry.
func (r *ResourceRegistry) Register(info ResourceInfo) {
	r.resources = append(r.resources, info)
}

// Resources returns all registered resources.
func (r *ResourceRegistry) Resources() []handlers.ResourceInfo {
	// Convert to handlers.ResourceInfo to avoid import cycles
	result := make([]handlers.ResourceInfo, len(r.resources))
	for i, res := range r.resources {
		result[i] = handlers.ResourceInfo{
			GVK:            res.GVK,
			Plural:         res.Plural,
			SchemaYAML:     res.SchemaYAML,
			NewObjectFunc:  res.NewObjectFunc,
			NewListFunc:    res.NewListFunc,
			PrivateNewFunc: res.PrivateNewFunc,
		}
	}
	return result
}

// GetResources returns the internal resource list.
func (r *ResourceRegistry) GetResources() []ResourceInfo {
	return r.resources
}

// CreateHandler creates a ResourceHandler for the given resource info.
func (r *ResourceRegistry) CreateHandler(store storage.ResourceStore, info ResourceInfo) (*handlers.ResourceHandler, error) {
	// Create schema processor
	processor, err := r.createProcessor(info.SchemaYAML)
	if err != nil {
		return nil, fmt.Errorf("failed to create processor for %s: %w", info.Plural, err)
	}

	// Create handler
	handler := handlers.NewResourceHandler(
		store,
		processor,
		info.GVK,
		info.Plural,
		info.NewObjectFunc,
		info.NewListFunc,
	)

	return handler, nil
}

// CreateConvertingHandler creates a ConvertingResourceHandler for the given resource info.
func (r *ResourceRegistry) CreateConvertingHandler(store storage.ResourceStore, converter interface{}, info ResourceInfo) (interface{}, error) {
	// Create schema processor
	processor, err := r.createProcessor(info.SchemaYAML)
	if err != nil {
		return nil, fmt.Errorf("failed to create processor for %s: %w", info.Plural, err)
	}

	// Create converting handler
	handler := handlers.NewConvertingResourceHandler(
		store,
		processor,
		converter.(*conversion.Converter),
		info.GVK,
		info.Plural,
		info.NewObjectFunc,
		info.NewListFunc,
		info.PrivateNewFunc,
	)

	return handler, nil
}

// createProcessor creates a schema processor from YAML schema.
func (r *ResourceRegistry) createProcessor(schemaYAML string) (*pkgschema.Processor, error) {
	// Parse YAML to v1 JSONSchemaProps
	var propsV1 apiextv1.JSONSchemaProps
	if err := yaml.Unmarshal([]byte(schemaYAML), &propsV1); err != nil {
		return nil, err
	}

	// Convert to internal JSONSchemaProps
	var props apiext.JSONSchemaProps
	if err := apiextv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(&propsV1, &props, nil); err != nil {
		return nil, err
	}

	// Create structural schema
	structural, err := schema.NewStructural(&props)
	if err != nil {
		return nil, err
	}

	// Create processor
	return pkgschema.NewProcessor(structural, &props)
}

// RegisterTestResources registers the private test API resources.
func RegisterTestResources(registry *ResourceRegistry) {
	// Register Object resource
	registry.Register(ResourceInfo{
		GVK: runtimeschema.GroupVersionKind{
			Group:   "test.orlop.thetechnick.ninja",
			Version: "v1",
			Kind:    "Object",
		},
		Plural:        privatev1.ObjectPlural,
		SchemaYAML:    privatev1.ObjectSchemaYAML,
		NewObjectFunc: func() runtime.Object { return &privatev1.Object{} },
		NewListFunc:   func() runtime.Object { return &privatev1.ObjectList{} },
	})

	// Register Other resource
	registry.Register(ResourceInfo{
		GVK: runtimeschema.GroupVersionKind{
			Group:   "test.orlop.thetechnick.ninja",
			Version: "v1",
			Kind:    "Other",
		},
		Plural:        privatev1.OtherPlural,
		SchemaYAML:    privatev1.OtherSchemaYAML,
		NewObjectFunc: func() runtime.Object { return &privatev1.Other{} },
		NewListFunc:   func() runtime.Object { return &privatev1.OtherList{} },
	})
}

// RegisterPublicResources registers the public test API resources.
func RegisterPublicResources(registry *ResourceRegistry) {
	// Register Object resource (public API with private storage)
	registry.Register(ResourceInfo{
		GVK: runtimeschema.GroupVersionKind{
			Group:   "test.orlop.thetechnick.ninja",
			Version: "v1",
			Kind:    "Object",
		},
		Plural:         publicv1.ObjectPlural,
		SchemaYAML:     publicv1.ObjectSchemaYAML,
		NewObjectFunc:  func() runtime.Object { return &publicv1.Object{} },
		NewListFunc:    func() runtime.Object { return &publicv1.ObjectList{} },
		PrivateNewFunc: func() runtime.Object { return &privatev1.Object{} },
	})

	// Register Other resource (public API with private storage)
	registry.Register(ResourceInfo{
		GVK: runtimeschema.GroupVersionKind{
			Group:   "test.orlop.thetechnick.ninja",
			Version: "v1",
			Kind:    "Other",
		},
		Plural:         publicv1.OtherPlural,
		SchemaYAML:     publicv1.OtherSchemaYAML,
		NewObjectFunc:  func() runtime.Object { return &publicv1.Other{} },
		NewListFunc:    func() runtime.Object { return &publicv1.OtherList{} },
		PrivateNewFunc: func() runtime.Object { return &privatev1.Other{} },
	})
}
