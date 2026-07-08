package apiserver

import (
	"fmt"

	"github.com/thetechnick/orlop/pkg/apiserver/conversion"
	"github.com/thetechnick/orlop/pkg/apiserver/handlers"
	pkgschema "github.com/thetechnick/orlop/pkg/apiserver/schema"
	"github.com/thetechnick/orlop/pkg/apiserver/storage"
	"github.com/thetechnick/orlop/pkg/apiserver/types"
	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

// ResourceInfo is re-exported from types package for convenience.
type ResourceInfo = types.ResourceInfo

// ResourceRegistry manages API resource registrations and their stores.
type ResourceRegistry struct {
	resources []ResourceInfo
	stores    map[string]storage.ResourceStore
	scheme    *runtime.Scheme
}

// NewResourceRegistry creates a new resource registry.
func NewResourceRegistry(scheme *runtime.Scheme) *ResourceRegistry {
	return &ResourceRegistry{
		resources: []ResourceInfo{},
		stores:    make(map[string]storage.ResourceStore),
		scheme:    scheme,
	}
}

// Register adds a resource to the registry and creates its store.
func (r *ResourceRegistry) Register(info ResourceInfo) {
	r.resources = append(r.resources, info)
	// Create store for this resource
	r.stores[info.Plural] = storage.NewMemoryStore(info.Plural, r.scheme, info.GVK)
}

// GetStore returns the store for a given resource plural name.
func (r *ResourceRegistry) GetStore(plural string) storage.ResourceStore {
	return r.stores[plural]
}

// GetStores returns all stores indexed by resource plural name.
func (r *ResourceRegistry) GetStores() map[string]storage.ResourceStore {
	return r.stores
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
func (r *ResourceRegistry) CreateHandler(info ResourceInfo) (*handlers.ResourceHandler, error) {
	// Get store for this resource
	store := r.GetStore(info.Plural)
	if store == nil {
		return nil, fmt.Errorf("no store found for resource %s", info.Plural)
	}

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
func (r *ResourceRegistry) CreateConvertingHandler(converter interface{}, info ResourceInfo) (interface{}, error) {
	// Get store for this resource
	store := r.GetStore(info.Plural)
	if store == nil {
		return nil, fmt.Errorf("no store found for resource %s", info.Plural)
	}

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
