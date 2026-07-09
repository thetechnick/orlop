# Server-Side Apply Implementation Plan

## Overview

Server-Side Apply (SSA) is a declarative configuration mechanism that enables multiple controllers and users to safely manage different fields of the same Kubernetes object without conflicts.

## Current State

The API server currently supports:
- ✅ JSON Merge Patch (`application/merge-patch+json`)
- ✅ Strategic Merge Patch (`application/strategic-merge-patch+json`) - falls back to merge patch
- ❌ JSON Patch (`application/json-patch+json`) - returns 415 Unsupported Media Type
- ❌ Server-Side Apply (`application/apply-patch+yaml`) - not implemented

## What is Server-Side Apply?

### Problem It Solves

**Without SSA (current):**
```
Controller A: Updates spec.replicas = 3
Controller B: Updates spec.template.image = "nginx:1.20"
Result: Last write wins, can overwrite each other's changes
```

**With SSA:**
```
Controller A: Manages spec.replicas field
Controller B: Manages spec.template.image field
Result: Each controller owns its fields, no conflicts
```

### Key Features

1. **Field-Level Ownership Tracking**
   - Each field knows which "field manager" owns it
   - Stored in `.metadata.managedFields`

2. **Conflict Detection**
   - Server detects when two managers try to own the same field
   - Returns 409 Conflict if field ownership conflicts

3. **Force Takeover**
   - `?force=true` allows taking ownership of fields from another manager
   - Useful for manual interventions

4. **Declarative**
   - Send desired state, server merges with existing
   - Only specified fields are managed

## Implementation Requirements

### 1. API Changes

**Endpoint:**
```
PATCH /apis/{group}/{version}/namespaces/{namespace}/{resource}/{name}
Content-Type: application/apply-patch+yaml
?fieldManager={manager-name}&force={true|false}
```

**Query Parameters:**
- `fieldManager` (required): Identifier for the entity applying changes
- `force` (optional, default=false): Take ownership even if another manager owns the field

**Response Codes:**
- `200 OK`: Apply succeeded
- `409 Conflict`: Field ownership conflict (when force=false)
- `400 Bad Request`: Invalid apply configuration
- `404 Not Found`: Object doesn't exist (use POST to create)

### 2. Dependencies

Already available in go.mod:
```go
k8s.io/apimachinery v0.36.2
sigs.k8s.io/structured-merge-diff/v6 v6.4.1
```

New imports needed:
```go
"k8s.io/apimachinery/pkg/util/managedfields"
"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
"sigs.k8s.io/structured-merge-diff/v6/typed"
```

### 3. Storage Changes

No storage changes required! Managed fields are stored in the standard metadata:

```go
type ObjectMeta struct {
    // ... existing fields ...
    
    // ManagedFields maps workflow-id and version to the set of fields
    // that are managed by that workflow. This is mostly for internal
    // housekeeping, and users typically shouldn't need to set or
    // understand this field.
    ManagedFields []ManagedFieldsEntry `json:"managedFields,omitempty"`
}

type ManagedFieldsEntry struct {
    Manager    string      `json:"manager,omitempty"`
    Operation  string      `json:"operation,omitempty"` // "Apply" or "Update"
    APIVersion string      `json:"apiVersion,omitempty"`
    Time       metav1.Time `json:"time,omitempty"`
    FieldsType string      `json:"fieldsType,omitempty"` // "FieldsV1"
    FieldsV1   *FieldsV1   `json:"fieldsV1,omitempty"`   // Actual field tracking
}
```

### 4. Handler Implementation

**File:** `pkg/apiserver/handlers/apply.go` (new)

```go
// ApplyPatch handles server-side apply PATCH requests
func (h *ResourceHandler) ApplyPatch(w http.ResponseWriter, r *http.Request) {
    namespace := chi.URLParam(r, "namespace")
    name := chi.URLParam(r, "name")
    
    // Extract query parameters
    fieldManager := r.URL.Query().Get("fieldManager")
    if fieldManager == "" {
        writeError(w, http.StatusBadRequest, "fieldManager query parameter is required")
        return
    }
    
    force := r.URL.Query().Get("force") == "true"
    
    // Read apply configuration
    applyBytes, err := io.ReadAll(r.Body)
    if err != nil {
        writeError(w, http.StatusBadRequest, fmt.Sprintf("failed to read body: %v", err))
        return
    }
    
    // Get existing object (if it exists)
    existing, err := h.store.Get(namespace, name)
    if err != nil && !errors.IsNotFound(err) {
        writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get object: %v", err))
        return
    }
    
    // Perform server-side apply
    result, err := h.applyManager.Apply(
        existing,           // current object (nil if doesn't exist)
        applyBytes,         // desired state
        fieldManager,       // who is applying
        force,              // force takeover?
    )
    if err != nil {
        if isConflictError(err) {
            writeError(w, http.StatusConflict, err.Error())
        } else {
            writeError(w, http.StatusBadRequest, err.Error())
        }
        return
    }
    
    // Update or create the object
    if existing == nil {
        err = h.store.Create(result)
    } else {
        err = h.store.Update(result)
    }
    
    // ... rest of handler
}
```

### 5. Apply Manager Component

**File:** `pkg/apiserver/apply/manager.go` (new)

```go
package apply

import (
    "k8s.io/apimachinery/pkg/util/managedfields"
    "sigs.k8s.io/structured-merge-diff/v6/typed"
)

type Manager struct {
    typeConverter  managedfields.TypeConverter
    objectConverter runtime.ObjectConvertor
    openAPISchema  *schema.Structural
}

func NewManager(scheme *runtime.Scheme, openAPISchema *schema.Structural) *Manager {
    return &Manager{
        typeConverter: managedfields.NewTypeConverter(openAPISchema, false),
        objectConverter: scheme,
        openAPISchema: openAPISchema,
    }
}

func (m *Manager) Apply(
    current runtime.Object,
    applyConfig []byte,
    fieldManager string,
    force bool,
) (runtime.Object, error) {
    // 1. Convert apply config to typed object
    // 2. Extract managed fields from current object
    // 3. Use structured-merge-diff to compute field ownership
    // 4. Detect conflicts (if !force)
    // 5. Merge apply config with current object
    // 6. Update managed fields metadata
    // 7. Return merged object
}
```

### 6. Integration Points

**Router (`pkg/apiserver/router.go`):**
```go
// PATCH endpoint needs to detect apply vs other patch types
func (h *ResourceHandler) Patch(w http.ResponseWriter, r *http.Request) {
    contentType := r.Header.Get("Content-Type")
    
    switch contentType {
    case "application/apply-patch+yaml", "application/apply-patch+json":
        h.ApplyPatch(w, r)  // NEW: Server-side apply
    case "application/merge-patch+json":
        h.MergePatch(w, r)  // Existing
    case "application/strategic-merge-patch+json":
        h.StrategicMergePatch(w, r)  // Existing
    default:
        h.MergePatch(w, r)  // Default
    }
}
```

**Resource Handler:**
- Add `applyManager *apply.Manager` field
- Initialize in `NewResourceHandler()`
- Pass OpenAPI schema from registry

## Implementation Phases

### Phase 1: Basic Apply Support (MVP)
- [ ] Create apply package with Manager
- [ ] Implement basic Apply() logic using managedfields
- [ ] Add ApplyPatch handler
- [ ] Update router to dispatch to ApplyPatch
- [ ] Add fieldManager query parameter validation
- [ ] Basic integration test

### Phase 2: Field Ownership Tracking
- [ ] Implement proper managedFields encoding/decoding
- [ ] Track field ownership per manager
- [ ] Store managedFields in object metadata
- [ ] Update existing PATCH to mark fields as "Update" operation

### Phase 3: Conflict Detection
- [ ] Implement conflict detection logic
- [ ] Return 409 Conflict with details
- [ ] Implement force=true takeover
- [ ] Add conflict resolution tests

### Phase 4: Advanced Features
- [ ] Support both YAML and JSON apply configs
- [ ] Dry-run support (`?dryRun=All`)
- [ ] Field validation during apply
- [ ] Integration with existing schema validation

## Testing Strategy

### Unit Tests
```go
func TestApplyManager_NoConflict(t *testing.T)
func TestApplyManager_ConflictDetection(t *testing.T)
func TestApplyManager_ForceOverwrite(t *testing.T)
func TestApplyManager_CreateObject(t *testing.T)
func TestApplyManager_PartialUpdate(t *testing.T)
```

### Integration Tests
```go
func TestServerSideApply_TwoManagers(t *testing.T)
func TestServerSideApply_ConflictResolution(t *testing.T)
func TestServerSideApply_ManagedFieldsTracking(t *testing.T)
```

### Compatibility Tests
```go
func TestKubectl_ServerSideApply(t *testing.T) {
    // Use kubectl apply --server-side against our API
}
```

## Examples

### Example 1: Basic Apply

```bash
# First apply by controller-a
curl -X PATCH "http://localhost:8080/.../objects/myobj?fieldManager=controller-a" \
  -H "Content-Type: application/apply-patch+yaml" \
  -d '
spec:
  replicas: 3
'

# Second apply by controller-b (different fields - no conflict)
curl -X PATCH "http://localhost:8080/.../objects/myobj?fieldManager=controller-b" \
  -H "Content-Type: application/apply-patch+yaml" \
  -d '
spec:
  image: nginx:1.20
'
```

Result:
```yaml
metadata:
  name: myobj
  managedFields:
  - manager: controller-a
    operation: Apply
    fieldsV1:
      f:spec:
        f:replicas: {}
  - manager: controller-b
    operation: Apply
    fieldsV1:
      f:spec:
        f:image: {}
spec:
  replicas: 3
  image: nginx:1.20
```

### Example 2: Conflict Detection

```bash
# controller-a owns spec.replicas
curl -X PATCH "http://localhost:8080/.../objects/myobj?fieldManager=controller-a" \
  -H "Content-Type: application/apply-patch+yaml" \
  -d 'spec: {replicas: 3}'

# controller-b tries to own spec.replicas (conflict!)
curl -X PATCH "http://localhost:8080/.../objects/myobj?fieldManager=controller-b" \
  -H "Content-Type: application/apply-patch+yaml" \
  -d 'spec: {replicas: 5}'
```

Response: `409 Conflict`
```json
{
  "kind": "Status",
  "status": "Failure",
  "message": "Apply failed with conflicts: spec.replicas is owned by controller-a",
  "code": 409
}
```

### Example 3: Force Takeover

```bash
# Take ownership with force=true
curl -X PATCH "http://localhost:8080/.../objects/myobj?fieldManager=controller-b&force=true" \
  -H "Content-Type: application/apply-patch+yaml" \
  -d 'spec: {replicas: 5}'
```

Response: `200 OK` (ownership transferred)

## Benefits

1. **Multi-Controller Safety**: Multiple controllers can manage the same object without conflicts
2. **Declarative**: Users specify desired state, not imperative changes
3. **Conflict Detection**: Automatic detection of field ownership conflicts
4. **Kubectl Compatibility**: Works with `kubectl apply --server-side`
5. **GitOps Friendly**: Better for declarative config management
6. **Partial Updates**: Only manage fields you care about

## Challenges

1. **Complexity**: Structured-merge-diff library is complex
2. **OpenAPI Schema**: Requires proper schema for type information
3. **Managed Fields Size**: Can grow large for complex objects
4. **Backward Compatibility**: Need to maintain existing PATCH behavior
5. **Testing**: Comprehensive tests needed for conflict scenarios

## References

- [Kubernetes Server-Side Apply](https://kubernetes.io/docs/reference/using-api/server-side-apply/)
- [KEP-555: Server-Side Apply](https://github.com/kubernetes/enhancements/tree/master/keps/sig-api-machinery/555-server-side-apply)
- [structured-merge-diff library](https://github.com/kubernetes-sigs/structured-merge-diff)
- [API Conventions - Apply](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#apply)

## Decision

**Recommendation**: Implement Server-Side Apply in phases

**Rationale**:
- Essential feature for multi-controller scenarios
- Kubernetes ecosystem expects SSA support
- Required dependencies already available
- Can be implemented incrementally without breaking existing PATCH

**Next Steps**:
1. Review this document
2. Create implementation tasks
3. Start with Phase 1 (Basic Apply Support)
4. Iterate based on testing and feedback
