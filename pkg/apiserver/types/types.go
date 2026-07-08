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
	// PrivateNewFunc creates a new instance of the private resource (for converting handlers)
	// This is only used for public API resources that need conversion
	PrivateNewFunc func() runtime.Object
}
