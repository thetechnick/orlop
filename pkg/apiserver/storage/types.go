package storage

import "sigs.k8s.io/controller-runtime/pkg/client"

// WatchEvent represents a resource change event with its resource version.
type WatchEvent struct {
	Type            string // ADDED, MODIFIED, DELETED, BOOKMARK
	Object          client.Object
	ResourceVersion string
}
