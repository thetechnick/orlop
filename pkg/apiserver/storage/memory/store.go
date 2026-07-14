package memory

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/thetechnick/orlop/pkg/apiserver/storage"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// MemoryStoreOption configures a MemoryStore during creation.
type MemoryStoreOption func(*MemoryStore)

// WithBroadcaster sets a custom storage.EventBroadcaster for the store.
// If not provided, defaults to in-memory Watcher.
func WithBroadcaster(broadcaster storage.EventBroadcaster) MemoryStoreOption {
	return func(s *MemoryStore) {
		s.broadcaster = broadcaster
	}
}

// WithBroadcasterFactory sets a factory function to create the storage.EventBroadcaster.
func WithBroadcasterFactory(factory storage.EventBroadcasterFactory) MemoryStoreOption {
	return func(s *MemoryStore) {
		s.broadcaster = factory()
	}
}

// NewMemoryStore creates a new MemoryStore for a specific resource type.
// Each store has its own resource version counter and event broadcaster.
//
// By default, uses in-memory Watcher. To use external database broadcasting:
//
//	store := NewMemoryStore("objects", scheme, gvk,
//	    WithBroadcaster(NewMongoDBBroadcaster(client, "db", "collection")))
func NewMemoryStore(resourceType string, scheme *runtime.Scheme, gvk schema.GroupVersionKind, opts ...MemoryStoreOption) *MemoryStore {
	store := &MemoryStore{
		resourceType: resourceType,
		objects:      make(map[string]client.Object),
		scheme:       scheme,
		gvk:          gvk,
	}

	// Apply options
	for _, opt := range opts {
		opt(store)
	}

	// Default to in-memory watcher if no broadcaster provided
	if store.broadcaster == nil {
		store.broadcaster = NewWatcher(50) // Buffer 50 events
	}

	return store
}

// MemoryStore implements ResourceStore using an in-memory map.
// Each MemoryStore instance is for a specific resource type and has its own
// resource version counter and event broadcaster.
//
// The broadcaster can be swapped to use external databases for event distribution.
type MemoryStore struct {
	mu                     sync.RWMutex
	resourceType           string
	objects                map[string]client.Object // namespace/name -> object
	scheme                 *runtime.Scheme          // For creating list objects
	gvk                    schema.GroupVersionKind  // For list metadata
	resourceVersionCounter atomic.Int64             // Per-resource version counter
	broadcaster            storage.EventBroadcaster         // Pluggable event broadcaster
}

// Create creates a new resource.
// If obj.GetName() is empty and obj.GetGenerateName() is set,
// a unique name is generated atomically under the lock.
func (s *MemoryStore) Create(obj client.Object) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	namespace := obj.GetNamespace()
	name := obj.GetName()

	if name == "" && obj.GetGenerateName() != "" {
		for range 5 {
			candidate := storage.GenerateName(obj.GetGenerateName())
			if _, exists := s.objects[s.makeKey(namespace, candidate)]; !exists {
				name = candidate
				obj.SetName(name)
				break
			}
		}
		if name == "" {
			return fmt.Errorf("failed to generate unique name after retries")
		}
	}

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
	watcher := s.broadcaster
	watcher.Broadcast(storage.ResourceEvent{
		Type:            storage.EventAdded,
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
func (s *MemoryStore) List(opts storage.ListOptions) (client.ObjectList, error) {
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

	// Parse label selector if specified
	var labelSelector labels.Selector
	if opts.LabelSelector != "" {
		labelSelector, err = labels.Parse(opts.LabelSelector)
		if err != nil {
			return nil, err
		}
	}

	// Parse continue token if specified
	var continueToken *storage.ContinueToken
	if opts.Continue != "" {
		continueToken, err = storage.DecodeContinueToken(opts.Continue)
		if err != nil {
			return nil, fmt.Errorf("invalid continue token: %w", err)
		}
	}

	// Collect all matching keys first, then sort for stable pagination
	var keys []string
	for key := range s.objects {
		// Filter by namespace
		if opts.Namespace != "" && !s.matchesNamespace(key, opts.Namespace) {
			continue
		}
		keys = append(keys, key)
	}

	// Sort keys for stable ordering (required for pagination)
	sort.Strings(keys)

	// Collect matching items with pagination support
	var items []runtime.Object
	var remainingAfterLimit int
	limit := opts.Limit
	if limit == 0 {
		limit = int64(len(keys)) // No limit means return all
	}

	for _, key := range keys {
		obj := s.objects[key]

		// Parse namespace and name from key for continue token comparison
		namespace, name := s.parseKey(key)

		// Skip items until we pass the continue token position
		if !storage.ShouldIncludeObject(namespace, name, continueToken) {
			continue
		}

		// Filter by label selector
		if labelSelector != nil {
			if !labelSelector.Matches(labels.Set(obj.GetLabels())) {
				continue
			}
		}

		// Filter by shard if specified
		if opts.ShardSelector != nil {
			matches, err := storage.MatchesShard(obj, opts.ShardSelector)
			if err != nil {
				continue
			}
			if !matches {
				continue
			}
		}

		// Check if we've reached the limit
		if int64(len(items)) >= limit {
			remainingAfterLimit++
			continue
		}

		items = append(items, obj.DeepCopyObject())
	}

	// Set items on the list using meta.SetList
	if err := meta.SetList(list, items); err != nil {
		return nil, fmt.Errorf("failed to set list items: %w", err)
	}

	// Set list metadata
	list.SetResourceVersion(s.currentResourceVersion())

	// Set continue token if there are more results
	if remainingAfterLimit > 0 && len(items) > 0 {
		listMeta, err := meta.ListAccessor(list)
		if err == nil {
			// Get the last item to create continue token
			lastItem := items[len(items)-1]
			lastAccessor, err := meta.Accessor(lastItem)
			if err == nil {
				token := &storage.ContinueToken{
					Namespace:       lastAccessor.GetNamespace(),
					Name:            lastAccessor.GetName(),
					ResourceVersion: s.currentResourceVersion(),
				}
				continueStr, err := storage.EncodeContinueToken(token)
				if err == nil {
					listMeta.SetContinue(continueStr)
					listMeta.SetRemainingItemCount(int64Ptr(int64(remainingAfterLimit)))
				}
			}
		}
	}

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
	watcher := s.broadcaster
	watcher.Broadcast(storage.ResourceEvent{
		Type:            storage.EventModified,
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
	watcher := s.broadcaster
	watcher.Broadcast(storage.ResourceEvent{
		Type:            storage.EventDeleted,
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

// parseKey extracts namespace and name from a storage key.
func (s *MemoryStore) parseKey(key string) (namespace, name string) {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	// No namespace (cluster-scoped)
	return "", key
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
func (s *MemoryStore) Watch(opts storage.ListOptions, resourceVersion string) (<-chan storage.ResourceEvent, func(), error) {
	watcher := s.broadcaster

	// Parse label selector if specified
	var labelSelector labels.Selector
	if opts.LabelSelector != "" {
		var err error
		labelSelector, err = labels.Parse(opts.LabelSelector)
		if err != nil {
			return nil, nil, err
		}
	}

	// Subscribe to watch events
	eventCh, stopSubscription, err := watcher.Subscribe(resourceVersion)
	if err != nil {
		return nil, nil, err
	}

	// Create filtered output channel
	outCh := make(chan storage.ResourceEvent, 100)
	stopCh := make(chan struct{})

	// Start filtering goroutine
	go func() {
		defer close(outCh)
		defer stopSubscription()

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
				if labelSelector != nil {
					accessor, err := meta.Accessor(event.Object)
					if err != nil {
						continue
					}
					if !labelSelector.Matches(labels.Set(accessor.GetLabels())) {
						continue
					}
				}

				// Filter by shard
				if opts.ShardSelector != nil {
					clientObj, ok := event.Object.(client.Object)
					if ok {
						matches, err := storage.MatchesShard(clientObj, opts.ShardSelector)
						if err != nil || !matches {
							continue
						}
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

// int64Ptr returns a pointer to an int64 value.
func int64Ptr(v int64) *int64 {
	return &v
}
