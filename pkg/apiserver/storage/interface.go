package storage

import (
	"k8s.io/apimachinery/pkg/runtime"
)

// ResourceStore defines the interface for storing and retrieving resources.
type ResourceStore interface {
	// Create creates a new resource with the given namespace and name.
	Create(resourceType, namespace, name string, obj runtime.Object) error

	// Get retrieves a resource by namespace and name.
	Get(resourceType, namespace, name string) (runtime.Object, error)

	// List lists all resources in the given namespace.
	// If namespace is empty, lists resources across all namespaces.
	List(resourceType, namespace string) ([]runtime.Object, error)

	// Update updates an existing resource.
	Update(resourceType, namespace, name string, obj runtime.Object) error

	// Delete deletes a resource by namespace and name.
	Delete(resourceType, namespace, name string) error
}
