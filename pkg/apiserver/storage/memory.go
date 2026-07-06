package storage

import (
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// MemoryStore implements ResourceStore using an in-memory map.
type MemoryStore struct {
	mu sync.RWMutex
	// objects[resourceType][namespace/name] = object
	objects           map[string]map[string]runtime.Object
	resourceVersionCounter int64
}

// NewMemoryStore creates a new in-memory storage backend.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		objects: make(map[string]map[string]runtime.Object),
		resourceVersionCounter: 0,
	}
}

// Create creates a new resource.
func (s *MemoryStore) Create(resourceType, namespace, name string, obj runtime.Object) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.makeKey(namespace, name)

	if s.objects[resourceType] == nil {
		s.objects[resourceType] = make(map[string]runtime.Object)
	}

	if _, exists := s.objects[resourceType][key]; exists {
		return errors.NewAlreadyExists(schema.GroupResource{Resource: resourceType}, name)
	}

	// Set resource version
	s.resourceVersionCounter++
	if err := s.setResourceVersion(obj, s.resourceVersionCounter); err != nil {
		return err
	}

	s.objects[resourceType][key] = obj.DeepCopyObject()
	return nil
}

// Get retrieves a resource.
func (s *MemoryStore) Get(resourceType, namespace, name string) (runtime.Object, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := s.makeKey(namespace, name)

	if s.objects[resourceType] == nil {
		return nil, errors.NewNotFound(schema.GroupResource{Resource: resourceType}, name)
	}

	obj, exists := s.objects[resourceType][key]
	if !exists {
		return nil, errors.NewNotFound(schema.GroupResource{Resource: resourceType}, name)
	}

	return obj.DeepCopyObject(), nil
}

// List lists all resources in a namespace.
func (s *MemoryStore) List(resourceType, namespace string) ([]runtime.Object, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []runtime.Object

	if s.objects[resourceType] == nil {
		return result, nil
	}

	for key, obj := range s.objects[resourceType] {
		if namespace == "" || s.matchesNamespace(key, namespace) {
			result = append(result, obj.DeepCopyObject())
		}
	}

	return result, nil
}

// Update updates an existing resource.
func (s *MemoryStore) Update(resourceType, namespace, name string, obj runtime.Object) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.makeKey(namespace, name)

	if s.objects[resourceType] == nil {
		return errors.NewNotFound(schema.GroupResource{Resource: resourceType}, name)
	}

	existing, exists := s.objects[resourceType][key]
	if !exists {
		return errors.NewNotFound(schema.GroupResource{Resource: resourceType}, name)
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
			schema.GroupResource{Resource: resourceType},
			name,
			fmt.Errorf("resource version mismatch: expected %s, got %s", existingRV, newRV),
		)
	}

	// Increment resource version
	s.resourceVersionCounter++
	if err := s.setResourceVersion(obj, s.resourceVersionCounter); err != nil {
		return err
	}

	s.objects[resourceType][key] = obj.DeepCopyObject()
	return nil
}

// Delete deletes a resource.
func (s *MemoryStore) Delete(resourceType, namespace, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.makeKey(namespace, name)

	if s.objects[resourceType] == nil {
		return errors.NewNotFound(schema.GroupResource{Resource: resourceType}, name)
	}

	if _, exists := s.objects[resourceType][key]; !exists {
		return errors.NewNotFound(schema.GroupResource{Resource: resourceType}, name)
	}

	delete(s.objects[resourceType], key)
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
