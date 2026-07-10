package handlers

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ShardSelector represents a shard selection for list/watch operations.
type ShardSelector struct {
	// Index is the shard index (0-based)
	Index int
	// Count is the total number of shards
	Count int
}

// ParseShardSelector parses shard parameters from query parameters.
// Returns nil if no shard selector is specified.
func ParseShardSelector(shardIndex, shardCount string) (*ShardSelector, error) {
	if shardIndex == "" && shardCount == "" {
		return nil, nil
	}

	if shardIndex == "" || shardCount == "" {
		return nil, fmt.Errorf("both shardIndex and shardCount must be specified")
	}

	index, err := strconv.Atoi(shardIndex)
	if err != nil {
		return nil, fmt.Errorf("invalid shardIndex: %w", err)
	}

	count, err := strconv.Atoi(shardCount)
	if err != nil {
		return nil, fmt.Errorf("invalid shardCount: %w", err)
	}

	if index < 0 {
		return nil, fmt.Errorf("shardIndex must be non-negative, got %d", index)
	}

	if count <= 0 {
		return nil, fmt.Errorf("shardCount must be positive, got %d", count)
	}

	if index >= count {
		return nil, fmt.Errorf("shardIndex (%d) must be less than shardCount (%d)", index, count)
	}

	return &ShardSelector{
		Index: index,
		Count: count,
	}, nil
}

// MatchesObject determines if the given object belongs to this shard.
func (s *ShardSelector) MatchesObject(obj client.Object) (bool, error) {
	if s == nil {
		return true, nil
	}

	shard, err := computeObjectShard(obj, s.Count)
	if err != nil {
		return false, err
	}

	return shard == s.Index, nil
}

// FilterList filters a list of objects to only include those in this shard.
func (s *ShardSelector) FilterList(items []interface{}) ([]interface{}, error) {
	if s == nil {
		return items, nil
	}

	filtered := make([]interface{}, 0, len(items)/s.Count)
	for _, item := range items {
		obj, ok := item.(client.Object)
		if !ok {
			// Skip items that aren't client.Object
			continue
		}

		matches, err := s.MatchesObject(obj)
		if err != nil {
			return nil, err
		}

		if matches {
			filtered = append(filtered, item)
		}
	}

	return filtered, nil
}

// computeObjectShard computes which shard (0 to shardCount-1) an object belongs to.
// Uses a hash of namespace/name to deterministically assign objects to shards.
func computeObjectShard(obj client.Object, shardCount int) (int, error) {
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return 0, err
	}

	// Use namespace and name to compute shard
	namespace := accessor.GetNamespace()
	name := accessor.GetName()

	// Compute stable hash of namespace/name
	h := sha256.New()
	h.Write([]byte(namespace))
	h.Write([]byte("/"))
	h.Write([]byte(name))
	hashBytes := h.Sum(nil)

	// Convert first 8 bytes to uint64
	hashValue := binary.BigEndian.Uint64(hashBytes[:8])

	// Modulo to get shard index
	return int(hashValue % uint64(shardCount)), nil
}

// String returns a human-readable representation of the shard selector.
func (s *ShardSelector) String() string {
	if s == nil {
		return "no shard"
	}
	return fmt.Sprintf("shard %d/%d", s.Index, s.Count)
}
