package storage

import (
	"container/ring"
	"fmt"
	"strconv"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// WatchEvent represents a resource change event with its resource version.
type WatchEvent struct {
	Type            string // ADDED, MODIFIED, DELETED
	Object          client.Object
	ResourceVersion string
}

// WatchBuffer stores recent events for a resource type to allow watch synchronization.
type WatchBuffer struct {
	mu     sync.RWMutex
	events *ring.Ring // Circular buffer of WatchEvent
	size   int
}

// NewWatchBuffer creates a new watch buffer with the specified capacity.
func NewWatchBuffer(size int) *WatchBuffer {
	return &WatchBuffer{
		events: ring.New(size),
		size:   size,
	}
}

// Add adds an event to the buffer.
func (wb *WatchBuffer) Add(event WatchEvent) {
	wb.mu.Lock()
	defer wb.mu.Unlock()

	wb.events.Value = event
	wb.events = wb.events.Next()
}

// GetEventsSince returns all events since the given resource version.
// Returns nil if the requested version is too old (not in buffer).
func (wb *WatchBuffer) GetEventsSince(resourceVersion string) ([]WatchEvent, error) {
	wb.mu.RLock()
	defer wb.mu.RUnlock()

	if resourceVersion == "" || resourceVersion == "0" {
		// Start from beginning - return all buffered events
		return wb.getAllEvents(), nil
	}

	requestedRV, err := strconv.ParseInt(resourceVersion, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid resourceVersion: %v", err)
	}

	var result []WatchEvent
	wb.events.Do(func(v interface{}) {
		if v == nil {
			return
		}
		event := v.(WatchEvent)
		eventRV, err := strconv.ParseInt(event.ResourceVersion, 10, 64)
		if err != nil {
			return
		}
		// Include events with RV > requested RV
		if eventRV > requestedRV {
			result = append(result, event)
		}
	})

	return result, nil
}

// getAllEvents returns all non-nil events in the buffer.
func (wb *WatchBuffer) getAllEvents() []WatchEvent {
	var result []WatchEvent
	wb.events.Do(func(v interface{}) {
		if v != nil {
			result = append(result, v.(WatchEvent))
		}
	})
	return result
}

// Watcher manages watch connections for a resource type.
type Watcher struct {
	mu          sync.RWMutex
	buffer      *WatchBuffer
	subscribers map[int]chan WatchEvent
	nextID      int
}

// NewWatcher creates a new watcher with event buffering.
func NewWatcher(bufferSize int) *Watcher {
	return &Watcher{
		buffer:      NewWatchBuffer(bufferSize),
		subscribers: make(map[int]chan WatchEvent),
	}
}

// Broadcast sends an event to all subscribers and adds it to the buffer.
func (w *Watcher) Broadcast(event WatchEvent) {
	// Add to buffer first
	w.buffer.Add(event)

	// Then broadcast to active watchers
	w.mu.RLock()
	defer w.mu.RUnlock()

	for _, ch := range w.subscribers {
		select {
		case ch <- event:
		default:
			// Don't block if subscriber is slow
		}
	}
}

// Subscribe creates a new watch channel and optionally sends historical events.
func (w *Watcher) Subscribe(sinceResourceVersion string) (<-chan WatchEvent, int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	id := w.nextID
	w.nextID++

	// Buffered channel to avoid blocking broadcaster
	ch := make(chan WatchEvent, 100)
	w.subscribers[id] = ch

	// Send historical events if requested
	if sinceResourceVersion != "" {
		events, err := w.buffer.GetEventsSince(sinceResourceVersion)
		if err != nil {
			close(ch)
			delete(w.subscribers, id)
			return nil, 0, err
		}

		// Send historical events
		for _, event := range events {
			select {
			case ch <- event:
			default:
				// Channel full, close it
				close(ch)
				delete(w.subscribers, id)
				return nil, 0, fmt.Errorf("watch channel overflow")
			}
		}
	}

	return ch, id, nil
}

// Unsubscribe removes a subscriber.
func (w *Watcher) Unsubscribe(id int) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if ch, exists := w.subscribers[id]; exists {
		close(ch)
		delete(w.subscribers, id)
	}
}

// Count returns the number of active subscribers.
func (w *Watcher) Count() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.subscribers)
}
