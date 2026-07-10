package storage

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ListOptions extends metav1.ListOptions with storage-specific options.
type ListOptions struct {
	metav1.ListOptions

	// Namespace limits results to a specific namespace.
	// Empty string means all namespaces.
	Namespace string

	// ShardSelector specifies which shard of results to return.
	// Nil means return all results (no sharding).
	ShardSelector *ShardSelector
}

// ShardSelector represents a shard selection for list/watch operations.
type ShardSelector struct {
	// Index is the shard index (0-based)
	Index int
	// Count is the total number of shards
	Count int
}

// ResourceStore defines the interface for storing and retrieving resources.
// Uses client.Object which combines metav1.Object and runtime.Object.
type ResourceStore interface {
	// Create creates a new resource.
	Create(obj client.Object) error

	// Get retrieves a resource by namespace and name.
	Get(namespace, name string) (client.Object, error)

	// List lists all resources matching the given options.
	// Returns a properly typed list object with metadata.
	// Storage implementations should handle:
	// - Namespace filtering
	// - Label selector filtering
	// - Shard-based filtering (if ShardSelector provided)
	List(opts ListOptions) (client.ObjectList, error)

	// Update updates an existing resource.
	Update(obj client.Object) error

	// Delete deletes a resource by namespace and name.
	Delete(namespace, name string) error

	// Watch starts watching for changes starting from the given resource version.
	// Returns a channel that receives watch events and a stop function to end the watch.
	// Storage implementations should filter events by:
	// - Namespace (if specified in opts)
	// - Label selector (if specified in opts)
	// - Shard (if ShardSelector provided in opts)
	Watch(opts ListOptions, resourceVersion string) (<-chan WatchEvent, func(), error)
}
