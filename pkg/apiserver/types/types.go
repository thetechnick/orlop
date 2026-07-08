package types

import (
	"k8s.io/apimachinery/pkg/runtime"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
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
