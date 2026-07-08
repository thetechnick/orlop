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
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NewMemoryStore creates a new MemoryStore for a specific resource type.
// Each store has its own resource version counter and watcher.
func NewMemoryStore(resourceType string, scheme *runtime.Scheme, gvk schema.GroupVersionKind) *MemoryStore {
	return &MemoryStore{
		resourceType: resourceType,
		objects:      make(map[string]client.Object),
		scheme:       scheme,
		gvk:          gvk,
		watcher:      NewWatcher(50), // Buffer 50 events
	}
}

// MemoryStore implements ResourceStore using an in-memory map.
// Each MemoryStore instance is for a specific resource type and has its own
// resource version counter and watcher.
type MemoryStore struct {
	mu                     sync.RWMutex
	resourceType           string
	objects                map[string]client.Object // namespace/name -> object
	scheme                 *runtime.Scheme          // For creating list objects
	gvk                    schema.GroupVersionKind  // For list metadata
	resourceVersionCounter atomic.Int64             // Per-resource version counter
	watcher                *Watcher                 // Per-resource watcher
}

// Create creates a new resource.
func (s *MemoryStore) Create(obj client.Object) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	namespace := obj.GetNamespace()
	name := obj.GetName()

	key := s.makeKey(namespace, name)

	if _, exists := s.objects[key]; exists {
		return errors.NewAlreadyExists(schema.GroupResource{Resource: s.resourceType}, name)
	}

	// Set resource version using store's counter
	newVersion := s.resourceVersionCounter.Add(1)
	if err := s.setResourceVersion(obj, newVersion); err != nil {
		return err
	}

	s.objects[key] = obj.DeepCopyObject().(client.Object)

	// Broadcast watch event
	watcher := s.watcher
	watcher.Broadcast(WatchEvent{
		Type:            "ADDED",
		Object:          obj.DeepCopyObject().(client.Object),
		ResourceVersion: fmt.Sprintf("%d", newVersion),
	})

	return nil
}

// Get retrieves a resource.
func (s *MemoryStore) Get(namespace, name string) (client.Object, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := s.makeKey(namespace, name)

	obj, exists := s.objects[key]
	if !exists {
		return nil, errors.NewNotFound(schema.GroupResource{Resource: s.resourceType}, name)
	}

	return obj.DeepCopyObject().(client.Object), nil
}

// List lists all resources matching the given options and returns a properly typed list object.
func (s *MemoryStore) List(opts client.ListOptions) (client.ObjectList, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Create list object using scheme
	listGVK := s.gvk.GroupVersion().WithKind(s.gvk.Kind + "List")
	listObj, err := s.scheme.New(listGVK)
	if err != nil {
		return nil, fmt.Errorf("failed to create list object: %w", err)
	}

	list, ok := listObj.(client.ObjectList)
	if !ok {
		return nil, fmt.Errorf("created object is not a client.ObjectList")
	}

	// Collect matching items
	var items []runtime.Object
	for key, obj := range s.objects {
		// Filter by namespace
		if opts.Namespace != "" && !s.matchesNamespace(key, opts.Namespace) {
			continue
		}

		// Filter by label selector
		// client.ListOptions uses LabelSelector which is a labels.Selector interface
		if opts.LabelSelector != nil {
			if !opts.LabelSelector.Matches(labels.Set(obj.GetLabels())) {
				continue
			}
		}

		items = append(items, obj.DeepCopyObject())
	}

	// Set items on the list using meta.SetList
	if err := meta.SetList(list, items); err != nil {
		return nil, fmt.Errorf("failed to set list items: %w", err)
	}

	// Set list metadata
	list.SetResourceVersion(s.currentResourceVersion())

	return list, nil
}

// currentResourceVersion returns the current resource version for this store.
func (s *MemoryStore) currentResourceVersion() string {
	return fmt.Sprintf("%d", s.resourceVersionCounter.Load())
}

// Update updates an existing resource.
func (s *MemoryStore) Update(obj client.Object) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	namespace := obj.GetNamespace()
	name := obj.GetName()
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

	// Increment resource version using store's counter
	newVersion := s.resourceVersionCounter.Add(1)
	if err := s.setResourceVersion(obj, newVersion); err != nil {
		return err
	}

	s.objects[key] = obj.DeepCopyObject().(client.Object)

	// Broadcast watch event
	watcher := s.watcher
	watcher.Broadcast(WatchEvent{
		Type:            "MODIFIED",
		Object:          obj.DeepCopyObject().(client.Object),
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
	watcher := s.watcher
	watcher.Broadcast(WatchEvent{
		Type:            "DELETED",
		Object:          obj.DeepCopyObject().(client.Object),
		ResourceVersion: s.currentResourceVersion(),
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

// Watch starts watching for changes starting from the given resource version.
func (s *MemoryStore) Watch(opts client.ListOptions, resourceVersion string) (<-chan WatchEvent, func(), error) {
	watcher := s.watcher

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
				if opts.Namespace != "" {
					accessor, err := meta.Accessor(event.Object)
					if err != nil {
						continue
					}
					if accessor.GetNamespace() != opts.Namespace {
						continue
					}
				}

				// Filter by label selector
				if opts.LabelSelector != nil {
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
