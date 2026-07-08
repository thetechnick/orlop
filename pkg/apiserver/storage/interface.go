package storage

import (
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ResourceStore defines the interface for storing and retrieving resources.
// Uses client.Object which combines metav1.Object and runtime.Object.
type ResourceStore interface {
	// Create creates a new resource.
	Create(obj client.Object) error

	// Get retrieves a resource by namespace and name.
	Get(namespace, name string) (client.Object, error)

	// List lists all resources matching the given options.
	// Returns a properly typed list object with metadata.
	// Uses client.ListOptions which supports:
	// - Namespace (empty for all namespaces)
	// - LabelSelector (client.MatchingLabels)
	// - FieldSelector (client.MatchingFields)
	// - Limit and Continue for pagination
	List(opts client.ListOptions) (client.ObjectList, error)

	// Update updates an existing resource.
	Update(obj client.Object) error

	// Delete deletes a resource by namespace and name.
	Delete(namespace, name string) error

	// Watch starts watching for changes starting from the given resource version.
	// Returns a channel that receives watch events and a stop function to end the watch.
	Watch(opts client.ListOptions, resourceVersion string) (<-chan WatchEvent, func(), error)
}
