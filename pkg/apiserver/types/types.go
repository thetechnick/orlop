package types

import (
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
}
