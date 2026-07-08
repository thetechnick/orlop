package storage

import (
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

// ListOptions contains options for listing resources.
type ListOptions struct {
	// LabelSelector filters resources by labels.
	LabelSelector labels.Selector
}

// ResourceStore defines the interface for storing and retrieving resources.
// Each ResourceStore instance is for a specific resource type.
type ResourceStore interface {
	// Create creates a new resource with the given namespace and name.
	Create(namespace, name string, obj runtime.Object) error

	// Get retrieves a resource by namespace and name.
	Get(namespace, name string) (runtime.Object, error)

	// List lists all resources in the given namespace.
	// If namespace is empty, lists resources across all namespaces.
	// If opts.LabelSelector is provided, filters by labels.
	List(namespace string, opts ListOptions) ([]runtime.Object, error)

	// Update updates an existing resource.
	Update(namespace, name string, obj runtime.Object) error

	// Delete deletes a resource by namespace and name.
	Delete(namespace, name string) error

	// CurrentResourceVersion returns the current resource version of the store.
	// This is used for list metadata to indicate the version at which the list was served.
	CurrentResourceVersion() string

	// Watch starts watching for changes starting from the given resource version.
	// Returns a channel that receives watch events and a stop function to end the watch.
	Watch(namespace string, opts ListOptions, resourceVersion string) (<-chan WatchEvent, func(), error)
}
