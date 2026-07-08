package storage

import (
	"fmt"
	"sync"
	"sync/atomic"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// MemoryBackend provides shared state for all MemoryStore instances.
type MemoryBackend struct {
	resourceVersionCounter atomic.Int64
	mu                     sync.RWMutex
	watchers               map[string]*Watcher // resourceType -> Watcher
}

// NewMemoryBackend creates a new memory backend with shared resource version counter.
func NewMemoryBackend() *MemoryBackend {
	return &MemoryBackend{
		watchers: make(map[string]*Watcher),
	}
}

// NewStore creates a new MemoryStore for a specific resource type that shares
// the resource version counter with other stores from this backend.
func (b *MemoryBackend) NewStore(resourceType string) *MemoryStore {
	return &MemoryStore{
		backend:      b,
		resourceType: resourceType,
		objects:      make(map[string]runtime.Object),
	}
}

// CurrentResourceVersion returns the current resource version.
func (b *MemoryBackend) CurrentResourceVersion() string {
	return fmt.Sprintf("%d", b.resourceVersionCounter.Load())
}

// GetWatcher returns the watcher for a resource type, creating one if needed.
func (b *MemoryBackend) GetWatcher(resourceType string) *Watcher {
	b.mu.RLock()
	watcher, exists := b.watchers[resourceType]
	b.mu.RUnlock()

	if exists {
		return watcher
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// Double-check after acquiring write lock
	if watcher, exists := b.watchers[resourceType]; exists {
		return watcher
	}

	watcher = NewWatcher(50) // Buffer 50 events
	b.watchers[resourceType] = watcher
	return watcher
}

// MemoryStore implements ResourceStore using an in-memory map.
// Each MemoryStore instance is for a specific resource type but shares
// a resource version counter with other stores from the same backend.
type MemoryStore struct {
	mu           sync.RWMutex
	backend      *MemoryBackend
	resourceType string
	objects      map[string]runtime.Object // namespace/name -> object
}

// Create creates a new resource.
func (s *MemoryStore) Create(namespace, name string, obj runtime.Object) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.makeKey(namespace, name)

	if _, exists := s.objects[key]; exists {
		return errors.NewAlreadyExists(schema.GroupResource{Resource: s.resourceType}, name)
	}

	// Set resource version using shared counter
	newVersion := s.backend.resourceVersionCounter.Add(1)
	if err := s.setResourceVersion(obj, newVersion); err != nil {
		return err
	}

	s.objects[key] = obj.DeepCopyObject()

	// Broadcast watch event
	watcher := s.backend.GetWatcher(s.resourceType)
	watcher.Broadcast(WatchEvent{
		Type:            "ADDED",
		Object:          obj.DeepCopyObject(),
		ResourceVersion: fmt.Sprintf("%d", newVersion),
	})

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

	// Increment resource version using shared counter
	newVersion := s.backend.resourceVersionCounter.Add(1)
	if err := s.setResourceVersion(obj, newVersion); err != nil {
		return err
	}

	s.objects[key] = obj.DeepCopyObject()

	// Broadcast watch event
	watcher := s.backend.GetWatcher(s.resourceType)
	watcher.Broadcast(WatchEvent{
		Type:            "MODIFIED",
		Object:          obj.DeepCopyObject(),
		ResourceVersion: fmt.Sprintf("%d", newVersion),
	})

	return nil
}

// Delete deletes a resource.
func (s *MemoryStore) Delete(namespace, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.makeKey(namespace, name)

	obj, exists := s.objects[key]
	if !exists {
		return errors.NewNotFound(schema.GroupResource{Resource: s.resourceType}, name)
	}

	delete(s.objects, key)

	// Broadcast watch event (use current RV since delete doesn't change it)
	watcher := s.backend.GetWatcher(s.resourceType)
	watcher.Broadcast(WatchEvent{
		Type:            "DELETED",
		Object:          obj.DeepCopyObject(),
		ResourceVersion: s.backend.CurrentResourceVersion(),
	})

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

// CurrentResourceVersion returns the current resource version of the backend.
func (s *MemoryStore) CurrentResourceVersion() string {
	return s.backend.CurrentResourceVersion()
}

// Watch starts watching for changes starting from the given resource version.
func (s *MemoryStore) Watch(namespace string, opts ListOptions, resourceVersion string) (<-chan WatchEvent, func(), error) {
	watcher := s.backend.GetWatcher(s.resourceType)

	// Subscribe to watch events
	eventCh, id, err := watcher.Subscribe(resourceVersion)
	if err != nil {
		return nil, nil, err
	}

	// Create filtered output channel
	outCh := make(chan WatchEvent, 100)
	stopCh := make(chan struct{})

	// Start filtering goroutine
	go func() {
		defer close(outCh)
		defer watcher.Unsubscribe(id)

		for {
			select {
			case <-stopCh:
				return
			case event, ok := <-eventCh:
				if !ok {
					return
				}

				// Filter by namespace
				if namespace != "" {
					accessor, err := meta.Accessor(event.Object)
					if err != nil {
						continue
					}
					if accessor.GetNamespace() != namespace {
						continue
					}
				}

				// Filter by label selector
				if opts.LabelSelector != nil && !opts.LabelSelector.Empty() {
					accessor, err := meta.Accessor(event.Object)
					if err != nil {
						continue
					}
					if !opts.LabelSelector.Matches(labels.Set(accessor.GetLabels())) {
						continue
					}
				}

				// Send filtered event
				select {
				case outCh <- event:
				case <-stopCh:
					return
				}
			}
		}
	}()

	stopFunc := func() {
		close(stopCh)
	}

	return outCh, stopFunc, nil
}
