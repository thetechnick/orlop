package storage

import (
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// MemoryStore implements ResourceStore using an in-memory map.
// Each MemoryStore instance is for a specific resource type.
type MemoryStore struct {
	mu                     sync.RWMutex
	resourceType           string
	objects                map[string]runtime.Object // namespace/name -> object
	resourceVersionCounter int64
}

// NewMemoryStore creates a new in-memory storage backend for a specific resource type.
func NewMemoryStore(resourceType string) *MemoryStore {
	return &MemoryStore{
		resourceType:           resourceType,
		objects:                make(map[string]runtime.Object),
		resourceVersionCounter: 0,
	}
}

// Create creates a new resource.
func (s *MemoryStore) Create(namespace, name string, obj runtime.Object) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.makeKey(namespace, name)

	if _, exists := s.objects[key]; exists {
		return errors.NewAlreadyExists(schema.GroupResource{Resource: s.resourceType}, name)
	}

	// Set resource version
	s.resourceVersionCounter++
	if err := s.setResourceVersion(obj, s.resourceVersionCounter); err != nil {
		return err
	}

	s.objects[key] = obj.DeepCopyObject()
	return nil
}

// Get retrieves a resource.
func (s *MemoryStore) Get(namespace, name string) (runtime.Object, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := s.makeKey(namespace, name)

	obj, exists := s.objects[key]
	if !exists {
		return nil, errors.NewNotFound(schema.GroupResource{Resource: s.resourceType}, name)
	}

	return obj.DeepCopyObject(), nil
}

// List lists all resources in a namespace.
func (s *MemoryStore) List(namespace string, opts ListOptions) ([]runtime.Object, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []runtime.Object

	for key, obj := range s.objects {
		// Filter by namespace
		if namespace != "" && !s.matchesNamespace(key, namespace) {
			continue
		}

		// Filter by label selector
		if opts.LabelSelector != nil && !opts.LabelSelector.Empty() {
			accessor, err := meta.Accessor(obj)
			if err != nil {
				continue
			}
			if !opts.LabelSelector.Matches(labels.Set(accessor.GetLabels())) {
				continue
			}
		}

		result = append(result, obj.DeepCopyObject())
	}

	return result, nil
}

// Update updates an existing resource.
func (s *MemoryStore) Update(namespace, name string, obj runtime.Object) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.makeKey(namespace, name)

	existing, exists := s.objects[key]
	if !exists {
		return errors.NewNotFound(schema.GroupResource{Resource: s.resourceType}, name)
	}

	// Check resource version for optimistic concurrency control
	existingRV, err := s.getResourceVersion(existing)
	if err != nil {
		return err
	}

	newRV, err := s.getResourceVersion(obj)
	if err != nil {
		return err
	}

	if newRV != existingRV {
		return errors.NewConflict(
			schema.GroupResource{Resource: s.resourceType},
			name,
			fmt.Errorf("resource version mismatch: expected %s, got %s", existingRV, newRV),
		)
	}

	// Increment resource version
	s.resourceVersionCounter++
	if err := s.setResourceVersion(obj, s.resourceVersionCounter); err != nil {
		return err
	}

	s.objects[key] = obj.DeepCopyObject()
	return nil
}

// Delete deletes a resource.
func (s *MemoryStore) Delete(namespace, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.makeKey(namespace, name)

	if _, exists := s.objects[key]; !exists {
		return errors.NewNotFound(schema.GroupResource{Resource: s.resourceType}, name)
	}

	delete(s.objects, key)
	return nil
}

// makeKey creates a storage key from namespace and name.
func (s *MemoryStore) makeKey(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "/" + name
}

// matchesNamespace checks if a key belongs to the given namespace.
func (s *MemoryStore) matchesNamespace(key, namespace string) bool {
	expectedPrefix := namespace + "/"
	return len(key) > len(expectedPrefix) && key[:len(expectedPrefix)] == expectedPrefix
}

// getResourceVersion extracts the resource version from an object.
func (s *MemoryStore) getResourceVersion(obj runtime.Object) (string, error) {
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return "", err
	}
	return accessor.GetResourceVersion(), nil
}

// setResourceVersion sets the resource version on an object.
func (s *MemoryStore) setResourceVersion(obj runtime.Object, version int64) error {
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return err
	}
	accessor.SetResourceVersion(fmt.Sprintf("%d", version))
	return nil
}
