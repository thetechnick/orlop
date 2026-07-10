package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/lib/pq"
	"github.com/thetechnick/orlop/pkg/apiserver/storage"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PostgresStore implements ResourceStore using SQL database (PostgreSQL).
// Stores resources as JSON in a single table with metadata columns for efficient querying.
type PostgresStore struct {
	db           *sql.DB
	resourceType string
	scheme       *runtime.Scheme
	gvk          schema.GroupVersionKind
	broadcaster  storage.EventBroadcaster
	tableName    string

	// For resource version generation
	mu                     sync.Mutex
	resourceVersionCounter int64
}

// PostgresStoreConfig configures SQL storage backend.
type PostgresStoreConfig struct {
	DB           *sql.DB
	ResourceType string
	Scheme       *runtime.Scheme
	GVK          schema.GroupVersionKind
	Broadcaster  storage.EventBroadcaster
	TableName    string // Optional: defaults to "resources_{resourceType}"
}

// NewPostgresStore creates a new SQL-backed resource store.
// Automatically creates the necessary table schema if it doesn't exist.
func NewPostgresStore(ctx context.Context, config PostgresStoreConfig) (*PostgresStore, error) {
	if config.DB == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	if config.ResourceType == "" {
		return nil, fmt.Errorf("resource type is required")
	}
	if config.Scheme == nil {
		return nil, fmt.Errorf("scheme is required")
	}

	tableName := config.TableName
	if tableName == "" {
		tableName = "resources_" + config.ResourceType
	}

	store := &PostgresStore{
		db:           config.DB,
		resourceType: config.ResourceType,
		scheme:       config.Scheme,
		gvk:          config.GVK,
		broadcaster:  config.Broadcaster,
		tableName:    tableName,
	}

	// Create table schema
	if err := store.createSchema(ctx); err != nil {
		return nil, fmt.Errorf("failed to create schema: %w", err)
	}

	// Initialize resource version counter
	if err := store.initResourceVersionCounter(ctx); err != nil {
		return nil, fmt.Errorf("failed to initialize resource version: %w", err)
	}

	return store, nil
}

// createSchema creates the necessary database tables.
func (s *PostgresStore) createSchema(ctx context.Context) error {
	schema := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id SERIAL PRIMARY KEY,
			namespace VARCHAR(253) NOT NULL,
			name VARCHAR(253) NOT NULL,
			resource_version BIGINT NOT NULL,
			labels JSONB,
			data JSONB NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
			UNIQUE(namespace, name)
		);

		CREATE INDEX IF NOT EXISTS idx_%s_namespace ON %s(namespace);
		CREATE INDEX IF NOT EXISTS idx_%s_resource_version ON %s(resource_version);
		CREATE INDEX IF NOT EXISTS idx_%s_labels ON %s USING GIN(labels);
	`, s.tableName,
		s.tableName, s.tableName,
		s.tableName, s.tableName,
		s.tableName, s.tableName)

	_, err := s.db.ExecContext(ctx, schema)
	return err
}

// initResourceVersionCounter initializes the counter from the database.
func (s *PostgresStore) initResourceVersionCounter(ctx context.Context) error {
	query := fmt.Sprintf("SELECT COALESCE(MAX(resource_version), 0) FROM %s", s.tableName)
	var maxRV int64
	err := s.db.QueryRowContext(ctx, query).Scan(&maxRV)
	if err != nil {
		return err
	}
	s.resourceVersionCounter = maxRV
	return nil
}

// nextResourceVersion generates the next resource version.
func (s *PostgresStore) nextResourceVersion() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resourceVersionCounter++
	return s.resourceVersionCounter
}

// Create implements ResourceStore.
func (s *PostgresStore) Create(obj client.Object) error {
	ctx := context.Background()

	namespace := obj.GetNamespace()
	name := obj.GetName()

	// Set resource version
	rv := s.nextResourceVersion()
	obj.SetResourceVersion(strconv.FormatInt(rv, 10))

	// Set timestamps if not set
	now := time.Now()
	creationTime := obj.GetCreationTimestamp()
	if creationTime.IsZero() {
		obj.SetCreationTimestamp(metav1.NewTime(now))
	}

	// Serialize to JSON
	data, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("failed to marshal object: %w", err)
	}

	// Serialize labels
	labelsJSON, err := json.Marshal(obj.GetLabels())
	if err != nil {
		return fmt.Errorf("failed to marshal labels: %w", err)
	}

	// Insert into database
	query := fmt.Sprintf(`
		INSERT INTO %s (namespace, name, resource_version, labels, data)
		VALUES ($1, $2, $3, $4, $5)
	`, s.tableName)

	_, err = s.db.ExecContext(ctx, query, namespace, name, rv, labelsJSON, data)
	if err != nil {
		// Check for unique constraint violation
		if isDuplicateKeyError(err) {
			return errors.NewAlreadyExists(
				schema.GroupResource{Resource: s.resourceType},
				name,
			)
		}
		return fmt.Errorf("failed to insert object: %w", err)
	}

	// Broadcast event
	if s.broadcaster != nil {
		s.broadcaster.Broadcast(storage.ResourceEvent{
			Type:            "ADDED",
			Object:          obj.DeepCopyObject().(client.Object),
			ResourceVersion: strconv.FormatInt(rv, 10),
		})
	}

	return nil
}

// Get implements ResourceStore.
func (s *PostgresStore) Get(namespace, name string) (client.Object, error) {
	ctx := context.Background()

	query := fmt.Sprintf(`
		SELECT data FROM %s
		WHERE namespace = $1 AND name = $2
	`, s.tableName)

	var data []byte
	err := s.db.QueryRowContext(ctx, query, namespace, name).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, errors.NewNotFound(
			schema.GroupResource{Resource: s.resourceType},
			name,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query object: %w", err)
	}

	// Deserialize
	obj := &unstructured.Unstructured{}
	if err := json.Unmarshal(data, obj); err != nil {
		return nil, fmt.Errorf("failed to unmarshal object: %w", err)
	}

	return obj, nil
}

// List implements ResourceStore.
func (s *PostgresStore) List(opts storage.ListOptions) (client.ObjectList, error) {
	ctx := context.Background()

	// Parse label selector if specified
	var labelSelector labels.Selector
	if opts.LabelSelector != "" {
		var err error
		labelSelector, err = labels.Parse(opts.LabelSelector)
		if err != nil {
			return nil, err
		}
	}

	// Build query with all filters at database level
	query := fmt.Sprintf("SELECT namespace, name, data FROM %s WHERE 1=1", s.tableName)
	args := []interface{}{}
	argNum := 1

	// Filter by namespace
	if opts.Namespace != "" {
		query += fmt.Sprintf(" AND namespace = $%d", argNum)
		args = append(args, opts.Namespace)
		argNum++
	}

	// Apply label selector using JSONB containment operator
	if opts.LabelSelector != "" {
		requirements, _ := labelSelector.Requirements()
		for _, req := range requirements {
			key := req.Key()
			values := req.Values()

			switch req.Operator() {
			case selection.Exists:
				// Label key must exist
				query += fmt.Sprintf(" AND labels ? $%d", argNum)
				args = append(args, key)
				argNum++
			case selection.DoesNotExist:
				// Label key must not exist
				query += fmt.Sprintf(" AND NOT (labels ? $%d)", argNum)
				args = append(args, key)
				argNum++
			case selection.Equals, selection.DoubleEquals, selection.In:
				// Label key must equal one of the values
				if values.Len() == 1 {
					// Single value: labels->>'key' = 'value'
					query += fmt.Sprintf(" AND labels->>$%d = $%d", argNum, argNum+1)
					args = append(args, key, values.List()[0])
					argNum += 2
				} else {
					// Multiple values: labels->>'key' IN ('v1', 'v2', ...)
					query += fmt.Sprintf(" AND labels->>$%d = ANY($%d)", argNum, argNum+1)
					args = append(args, key, pq.Array(values.List()))
					argNum += 2
				}
			case selection.NotEquals, selection.NotIn:
				// Label key must not equal any of the values
				if values.Len() == 1 {
					query += fmt.Sprintf(" AND (NOT (labels ? $%d) OR labels->>$%d != $%d)", argNum, argNum, argNum+1)
					args = append(args, key, values.List()[0])
					argNum += 2
				} else {
					query += fmt.Sprintf(" AND (NOT (labels ? $%d) OR NOT (labels->>$%d = ANY($%d)))", argNum, argNum, argNum+1)
					args = append(args, key, pq.Array(values.List()))
					argNum += 2
				}
			}
		}
	}

	// Apply shard selector using PostgreSQL SHA-256 hash
	if opts.ShardSelector != nil {
		// Replicate the Go shard computation logic in SQL:
		// 1. Concatenate namespace + "/" + name
		// 2. SHA-256 hash the result
		// 3. Take first 8 bytes as big-endian uint64
		// 4. Modulo by shard count
		// 5. Check if equals shard index
		//
		// PostgreSQL: get_byte(sha256(...), 0) gets the first byte
		// We need to reconstruct the uint64 from 8 bytes in big-endian order
		query += fmt.Sprintf(`
			AND (
				(get_byte(sha256((namespace || '/' || name)::bytea), 0)::bigint << 56) |
				(get_byte(sha256((namespace || '/' || name)::bytea), 1)::bigint << 48) |
				(get_byte(sha256((namespace || '/' || name)::bytea), 2)::bigint << 40) |
				(get_byte(sha256((namespace || '/' || name)::bytea), 3)::bigint << 32) |
				(get_byte(sha256((namespace || '/' || name)::bytea), 4)::bigint << 24) |
				(get_byte(sha256((namespace || '/' || name)::bytea), 5)::bigint << 16) |
				(get_byte(sha256((namespace || '/' || name)::bytea), 6)::bigint << 8) |
				get_byte(sha256((namespace || '/' || name)::bytea), 7)::bigint
			) %% $%d = $%d`, argNum, argNum+1)
		args = append(args, opts.ShardSelector.Count, opts.ShardSelector.Index)
		argNum += 2
	}

	// Apply continue token for pagination
	if opts.Continue != "" {
		continueToken, err := storage.DecodeContinueToken(opts.Continue)
		if err != nil {
			return nil, fmt.Errorf("invalid continue token: %w", err)
		}

		// Resume after the last returned object using lexicographic ordering
		if continueToken.Namespace != "" {
			// (namespace, name) > (continueToken.Namespace, continueToken.Name)
			query += fmt.Sprintf(" AND (namespace, name) > ($%d, $%d)", argNum, argNum+1)
			args = append(args, continueToken.Namespace, continueToken.Name)
			argNum += 2
		} else {
			// Cluster-scoped resources: name > continueToken.Name
			query += fmt.Sprintf(" AND name > $%d", argNum)
			args = append(args, continueToken.Name)
			argNum++
		}
	}

	// Order by namespace and name for stable pagination
	query += " ORDER BY namespace, name"

	// Apply limit for pagination
	var queryLimit int64
	if opts.Limit > 0 {
		// Fetch one extra to determine if there are more results
		queryLimit = opts.Limit + 1
		query += fmt.Sprintf(" LIMIT $%d", argNum)
		args = append(args, queryLimit)
		argNum++
	}

	// Execute query
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query objects: %w", err)
	}
	defer rows.Close()

	// Collect results
	var items []unstructured.Unstructured
	var maxRV int64
	rowCount := int64(0)

	for rows.Next() {
		rowCount++

		var namespace, name string
		var data []byte
		if err := rows.Scan(&namespace, &name, &data); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		// Stop if we've reached the limit (the extra row is for hasMore detection)
		if opts.Limit > 0 && rowCount > opts.Limit {
			break
		}

		obj := &unstructured.Unstructured{}
		if err := json.Unmarshal(data, obj); err != nil {
			return nil, fmt.Errorf("failed to unmarshal object: %w", err)
		}

		// Shard filtering is now done at the database level via SQL
		// No need to filter in-memory

		items = append(items, *obj)

		// Track max resource version
		rv, _ := strconv.ParseInt(obj.GetResourceVersion(), 10, 64)
		if rv > maxRV {
			maxRV = rv
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	// Create list object
	listGVK := s.gvk.GroupVersion().WithKind(s.gvk.Kind + "List")
	listObj, err := s.scheme.New(listGVK)
	if err != nil {
		return nil, fmt.Errorf("failed to create list object: %w", err)
	}

	list := listObj.(*unstructured.UnstructuredList)
	list.SetResourceVersion(strconv.FormatInt(maxRV, 10))
	list.Items = items

	// Set continue token if there are more results
	hasMore := opts.Limit > 0 && rowCount > opts.Limit
	if hasMore && len(items) > 0 {
		listMeta, err := meta.ListAccessor(list)
		if err == nil {
			lastItem := &items[len(items)-1]
			token := &storage.ContinueToken{
				Namespace:       lastItem.GetNamespace(),
				Name:            lastItem.GetName(),
				ResourceVersion: strconv.FormatInt(maxRV, 10),
			}
			continueStr, err := storage.EncodeContinueToken(token)
			if err == nil {
				listMeta.SetContinue(continueStr)
				// Note: remainingItemCount is not easily calculable without a full count query
			}
		}
	}

	return list, nil
}

// Update implements ResourceStore.
func (s *PostgresStore) Update(obj client.Object) error {
	ctx := context.Background()

	namespace := obj.GetNamespace()
	name := obj.GetName()

	// Get current object to check existence
	_, err := s.Get(namespace, name)
	if err != nil {
		return err
	}

	// Set new resource version
	rv := s.nextResourceVersion()
	obj.SetResourceVersion(strconv.FormatInt(rv, 10))

	// Serialize
	data, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("failed to marshal object: %w", err)
	}

	labelsJSON, err := json.Marshal(obj.GetLabels())
	if err != nil {
		return fmt.Errorf("failed to marshal labels: %w", err)
	}

	// Update in database
	query := fmt.Sprintf(`
		UPDATE %s
		SET resource_version = $1, labels = $2, data = $3, updated_at = NOW()
		WHERE namespace = $4 AND name = $5
	`, s.tableName)

	result, err := s.db.ExecContext(ctx, query, rv, labelsJSON, data, namespace, name)
	if err != nil {
		return fmt.Errorf("failed to update object: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return errors.NewNotFound(
			schema.GroupResource{Resource: s.resourceType},
			name,
		)
	}

	// Broadcast event
	if s.broadcaster != nil {
		s.broadcaster.Broadcast(storage.ResourceEvent{
			Type:            "MODIFIED",
			Object:          obj.DeepCopyObject().(client.Object),
			ResourceVersion: strconv.FormatInt(rv, 10),
		})
	}

	return nil
}

// Delete implements ResourceStore.
func (s *PostgresStore) Delete(namespace, name string) error {
	ctx := context.Background()

	// Get object before deleting for the event
	obj, err := s.Get(namespace, name)
	if err != nil {
		return err
	}

	// Delete from database
	query := fmt.Sprintf(`
		DELETE FROM %s
		WHERE namespace = $1 AND name = $2
	`, s.tableName)

	result, err := s.db.ExecContext(ctx, query, namespace, name)
	if err != nil {
		return fmt.Errorf("failed to delete object: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return errors.NewNotFound(
			schema.GroupResource{Resource: s.resourceType},
			name,
		)
	}

	// Broadcast event
	if s.broadcaster != nil {
		rv := s.nextResourceVersion()
		s.broadcaster.Broadcast(storage.ResourceEvent{
			Type:            "DELETED",
			Object:          obj,
			ResourceVersion: strconv.FormatInt(rv, 10),
		})
	}

	return nil
}

// Watch implements ResourceStore.
func (s *PostgresStore) Watch(opts storage.ListOptions, resourceVersion string) (<-chan storage.ResourceEvent, func(), error) {
	if s.broadcaster == nil {
		return nil, nil, fmt.Errorf("broadcaster not configured")
	}

	// Subscribe to broadcaster
	eventCh, stopSubscription, err := s.broadcaster.Subscribe(resourceVersion)
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

		// Parse label selector
		var labelSelector labels.Selector
		if opts.LabelSelector != "" {
			var err error
			labelSelector, err = labels.Parse(opts.LabelSelector)
			if err != nil {
				// Log error but continue watching
				labelSelector = nil
			}
		}

		for {
			select {
			case <-stopCh:
				return
			case event, ok := <-eventCh:
				if !ok {
					return
				}

				// Apply filters
				clientObj, ok := event.Object.(client.Object)
				if !ok {
					continue
				}

				// Filter by namespace
				if opts.Namespace != "" && clientObj.GetNamespace() != opts.Namespace {
					continue
				}

				// Filter by label selector
				if labelSelector != nil && !labelSelector.Matches(labels.Set(clientObj.GetLabels())) {
					continue
				}

				// Filter by shard
				if opts.ShardSelector != nil {
					matches, err := storage.MatchesShard(clientObj, opts.ShardSelector)
					if err != nil || !matches {
						continue
					}
				}

				select {
				case outCh <- event:
				case <-stopCh:
					return
				}
			}
		}
	}()

	// Stop function
	stopFunc := func() {
		close(stopCh)
	}

	return outCh, stopFunc, nil
}

// isDuplicateKeyError checks if the error is a duplicate key constraint violation.
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	// PostgreSQL error code 23505 is unique_violation
	return err.Error() != "" && (
		// Check for common duplicate key error messages
		contains(err.Error(), "duplicate key") ||
		contains(err.Error(), "unique constraint") ||
		contains(err.Error(), "23505"))
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) &&
		(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
		 findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Verify PostgresStore implements ResourceStore.
var _ storage.ResourceStore = (*PostgresStore)(nil)
