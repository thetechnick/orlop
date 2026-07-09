package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/thetechnick/orlop/pkg/apiserver/schema"
	"github.com/thetechnick/orlop/pkg/apiserver/storage"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ResourceHandler handles CRUD operations for a specific resource type.
type ResourceHandler struct {
	store        storage.ResourceStore
	processor    *schema.Processor
	gvk          runtimeschema.GroupVersionKind
	resourceType string
	scheme       *runtime.Scheme
}

// NewResourceHandler creates a new resource handler.
func NewResourceHandler(
	store storage.ResourceStore,
	processor *schema.Processor,
	gvk runtimeschema.GroupVersionKind,
	resourceType string,
	scheme *runtime.Scheme,
) *ResourceHandler {
	return &ResourceHandler{
		store:        store,
		processor:    processor,
		gvk:          gvk,
		resourceType: resourceType,
		scheme:       scheme,
	}
}

// Create handles POST requests to create a new resource.
func (h *ResourceHandler) Create(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	log.Printf("[CREATE] %s namespace=%s", h.gvk.Kind, namespace)

	// Parse request body as map for schema processing
	var objMap map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&objMap); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	// Process object (prune, default, validate)
	if errs := h.processor.Process(objMap); len(errs) > 0 {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("validation failed: %v", errs.ToAggregate()))
		return
	}

	// Convert back to typed object
	objJSON, _ := json.Marshal(objMap)
	obj, err := h.scheme.New(h.gvk)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create object: %v", err))
		return
	}
	if err := json.Unmarshal(objJSON, obj); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to unmarshal object: %v", err))
		return
	}
	clientObj := obj.(client.Object)

	// Set metadata
	accessor, err := meta.Accessor(clientObj)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to access metadata: %v", err))
		return
	}

	name := accessor.GetName()
	if name == "" {
		writeError(w, http.StatusBadRequest, "metadata.name is required")
		return
	}

	accessor.SetNamespace(namespace)
	accessor.SetUID(types.UID(uuid.New().String()))
	accessor.SetCreationTimestamp(metav1.Time{Time: time.Now()})
	accessor.SetGeneration(1)

	// Set GVK
	clientObj.GetObjectKind().SetGroupVersionKind(h.gvk)

	// Store object
	if err := h.store.Create(clientObj); err != nil {
		log.Printf("[CREATE] %s namespace=%s name=%s error=%v", h.gvk.Kind, namespace, name, err)
		if errors.IsAlreadyExists(err) {
			writeError(w, http.StatusConflict, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create object: %v", err))
		}
		return
	}

	log.Printf("[CREATE] %s namespace=%s name=%s status=created", h.gvk.Kind, namespace, name)

	// Return created object
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(clientObj)
}

// Get handles GET requests to retrieve a single resource.
func (h *ResourceHandler) Get(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	log.Printf("[GET] %s namespace=%s name=%s", h.gvk.Kind, namespace, name)

	obj, err := h.store.Get(namespace, name)
	if err != nil {
		log.Printf("[GET] %s namespace=%s name=%s error=%v", h.gvk.Kind, namespace, name, err)
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get object: %v", err))
		}
		return
	}

	log.Printf("[GET] %s namespace=%s name=%s status=found", h.gvk.Kind, namespace, name)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(obj)
}

// List handles GET requests to list resources.
func (h *ResourceHandler) List(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	if namespace == "" {
		log.Printf("[LIST] %s scope=cluster", h.gvk.Kind)
	} else {
		log.Printf("[LIST] %s namespace=%s", h.gvk.Kind, namespace)
	}

	// Build list options from query parameters
	opts := client.ListOptions{
		Namespace: namespace,
	}

	// Parse label selector from query parameter
	if labelSelectorStr := r.URL.Query().Get("labelSelector"); labelSelectorStr != "" {
		selector, err := labels.Parse(labelSelectorStr)
		if err != nil {
			if namespace == "" {
				log.Printf("[LIST] %s scope=cluster error=invalid-label-selector", h.gvk.Kind)
			} else {
				log.Printf("[LIST] %s namespace=%s error=invalid-label-selector", h.gvk.Kind, namespace)
			}
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid label selector: %v", err))
			return
		}
		opts.LabelSelector = selector
	}

	// Check if this is a watch request
	if r.URL.Query().Get("watch") == "true" {
		if namespace == "" {
			log.Printf("[WATCH] %s scope=cluster", h.gvk.Kind)
		} else {
			log.Printf("[WATCH] %s namespace=%s", h.gvk.Kind, namespace)
		}
		h.handleWatch(w, r, opts)
		return
	}

	list, err := h.store.List(opts)
	if err != nil {
		if namespace == "" {
			log.Printf("[LIST] %s scope=cluster error=%v", h.gvk.Kind, err)
		} else {
			log.Printf("[LIST] %s namespace=%s error=%v", h.gvk.Kind, namespace, err)
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list objects: %v", err))
		return
	}

	// Set GVK on the list
	list.GetObjectKind().SetGroupVersionKind(h.gvk.GroupVersion().WithKind(h.gvk.Kind + "List"))

	listMeta, _ := meta.ListAccessor(list)
	count := 0
	if listMeta != nil {
		items, _ := meta.ExtractList(list)
		count = len(items)
	}
	if namespace == "" {
		log.Printf("[LIST] %s scope=cluster count=%d", h.gvk.Kind, count)
	} else {
		log.Printf("[LIST] %s namespace=%s count=%d", h.gvk.Kind, namespace, count)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(list)
}

// handleWatch handles watch requests using Server-Sent Events.
func (h *ResourceHandler) handleWatch(w http.ResponseWriter, r *http.Request, opts client.ListOptions) {
	// Get resourceVersion to start from
	resourceVersion := r.URL.Query().Get("resourceVersion")

	// Start watch
	eventCh, stop, err := h.store.Watch(opts, resourceVersion)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("failed to start watch: %v", err))
		return
	}
	defer stop()

	// Set headers for streaming
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Transfer-Encoding", "chunked")

	// Get flusher for streaming
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	w.WriteHeader(http.StatusOK)
	// CRITICAL: Flush immediately after WriteHeader to send headers to client
	// This allows the client's Do() to return before we start streaming events
	flusher.Flush()

	// Stream events
	encoder := json.NewEncoder(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-eventCh:
			if !ok {
				return
			}

			// Send watch event
			watchEvent := map[string]interface{}{
				"type":   event.Type,
				"object": event.Object,
			}

			if err := encoder.Encode(watchEvent); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// Update handles PUT requests to update a resource.
func (h *ResourceHandler) Update(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	log.Printf("[UPDATE] %s namespace=%s name=%s", h.gvk.Kind, namespace, name)

	// Get existing object to compare spec
	existing, err := h.store.Get(namespace, name)
	if err != nil {
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get existing object: %v", err))
		}
		return
	}

	// Parse request body as map for schema processing
	var objMap map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&objMap); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	// Process object (prune, default, validate)
	if errs := h.processor.Process(objMap); len(errs) > 0 {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("validation failed: %v", errs.ToAggregate()))
		return
	}

	// Convert back to typed object
	objJSON, _ := json.Marshal(objMap)
	obj, err := h.scheme.New(h.gvk)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create object: %v", err))
		return
	}
	if err := json.Unmarshal(objJSON, obj); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to unmarshal object: %v", err))
		return
	}
	clientObj := obj.(client.Object)

	// Set metadata
	accessor, err := meta.Accessor(clientObj)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to access metadata: %v", err))
		return
	}

	accessor.SetNamespace(namespace)
	accessor.SetName(name)

	// Check if spec changed and increment generation if so
	existingAccessor, _ := meta.Accessor(existing)
	if specChanged(existing, clientObj) {
		accessor.SetGeneration(existingAccessor.GetGeneration() + 1)
	} else {
		accessor.SetGeneration(existingAccessor.GetGeneration())
	}

	// Set GVK
	clientObj.GetObjectKind().SetGroupVersionKind(h.gvk)

	// Update object
	if err := h.store.Update(clientObj); err != nil {
		log.Printf("[UPDATE] %s namespace=%s name=%s error=%v", h.gvk.Kind, namespace, name, err)
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
		} else if errors.IsConflict(err) {
			writeError(w, http.StatusConflict, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to update object: %v", err))
		}
		return
	}

	log.Printf("[UPDATE] %s namespace=%s name=%s status=updated", h.gvk.Kind, namespace, name)

	// Return updated object
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(clientObj)
}

// Patch handles PATCH requests to partially update a resource.
func (h *ResourceHandler) Patch(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	contentType := r.Header.Get("Content-Type")
	log.Printf("[PATCH] %s namespace=%s name=%s content-type=%s", h.gvk.Kind, namespace, name, contentType)

	// Get existing object
	existing, err := h.store.Get(namespace, name)
	if err != nil {
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get object: %v", err))
		}
		return
	}

	// Read patch body
	patchBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("failed to read patch: %v", err))
		return
	}

	// Convert existing object to JSON
	existingJSON, err := json.Marshal(existing)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to marshal existing object: %v", err))
		return
	}

	// Determine patch type from Content-Type header (already retrieved above for logging)
	var patchedJSON []byte

	switch contentType {
	case "application/json-patch+json":
		// JSON Patch (RFC 6902)
		writeError(w, http.StatusUnsupportedMediaType, "JSON Patch not yet supported")
		return
	case "application/merge-patch+json":
		// JSON Merge Patch (RFC 7386)
		patchedJSON, err = jsonMergePatch(existingJSON, patchBytes)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("merge patch failed: %v", err))
			return
		}
	case "application/strategic-merge-patch+json":
		// Strategic Merge Patch (Kubernetes default)
		// For now, use simple merge patch as strategic merge requires schema knowledge
		patchedJSON, err = jsonMergePatch(existingJSON, patchBytes)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("strategic merge patch failed: %v", err))
			return
		}
	default:
		// Default to merge patch
		patchedJSON, err = jsonMergePatch(existingJSON, patchBytes)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("patch failed: %v", err))
			return
		}
	}

	// Convert to map for schema processing
	var objMap map[string]interface{}
	if err := json.Unmarshal(patchedJSON, &objMap); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to unmarshal patched object: %v", err))
		return
	}

	// Process object (prune, default, validate)
	if errs := h.processor.Process(objMap); len(errs) > 0 {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("validation failed: %v", errs.ToAggregate()))
		return
	}

	// Convert back to typed object
	objJSON, _ := json.Marshal(objMap)
	obj, err := h.scheme.New(h.gvk)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create object: %v", err))
		return
	}
	if err := json.Unmarshal(objJSON, obj); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to unmarshal object: %v", err))
		return
	}
	clientObj := obj.(client.Object)

	// Set metadata
	accessor, err := meta.Accessor(clientObj)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to access metadata: %v", err))
		return
	}

	accessor.SetNamespace(namespace)
	accessor.SetName(name)

	// Check if spec changed and increment generation if so
	existingAccessor, _ := meta.Accessor(existing)
	if specChanged(existing, clientObj) {
		accessor.SetGeneration(existingAccessor.GetGeneration() + 1)
	} else {
		accessor.SetGeneration(existingAccessor.GetGeneration())
	}

	// Set GVK
	clientObj.GetObjectKind().SetGroupVersionKind(h.gvk)

	// Update object in storage
	if err := h.store.Update(clientObj); err != nil {
		log.Printf("[PATCH] %s namespace=%s name=%s error=%v", h.gvk.Kind, namespace, name, err)
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
		} else if errors.IsConflict(err) {
			writeError(w, http.StatusConflict, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to update object: %v", err))
		}
		return
	}

	log.Printf("[PATCH] %s namespace=%s name=%s status=patched", h.gvk.Kind, namespace, name)

	// Return patched object
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(clientObj)
}

// Delete handles DELETE requests to delete a resource.
func (h *ResourceHandler) Delete(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	log.Printf("[DELETE] %s namespace=%s name=%s", h.gvk.Kind, namespace, name)

	if err := h.store.Delete(namespace, name); err != nil {
		log.Printf("[DELETE] %s namespace=%s name=%s error=%v", h.gvk.Kind, namespace, name, err)
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to delete object: %v", err))
		}
		return
	}

	log.Printf("[DELETE] %s namespace=%s name=%s status=deleted", h.gvk.Kind, namespace, name)

	// Return success status
	status := metav1.Status{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Status",
		},
		Status: metav1.StatusSuccess,
		Code:   http.StatusOK,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(status)
}

// writeError writes an error response with a Status object.
func writeError(w http.ResponseWriter, code int, message string) {
	status := metav1.Status{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Status",
		},
		Status:  metav1.StatusFailure,
		Message: message,
		Code:    int32(code),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(status)
}

// specChanged checks if the spec field has changed between two objects.
func specChanged(old, new runtime.Object) bool {
	oldJSON, _ := json.Marshal(old)
	newJSON, _ := json.Marshal(new)

	var oldMap, newMap map[string]interface{}
	json.Unmarshal(oldJSON, &oldMap)
	json.Unmarshal(newJSON, &newMap)

	oldSpec, _ := json.Marshal(oldMap["spec"])
	newSpec, _ := json.Marshal(newMap["spec"])

	return string(oldSpec) != string(newSpec)
}

// jsonMergePatch applies a JSON merge patch (RFC 7386) to original JSON.
func jsonMergePatch(original, patch []byte) ([]byte, error) {
	var originalMap, patchMap map[string]interface{}

	if err := json.Unmarshal(original, &originalMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal original: %w", err)
	}

	if err := json.Unmarshal(patch, &patchMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal patch: %w", err)
	}

	// Apply merge patch
	merged := mergeMaps(originalMap, patchMap)

	return json.Marshal(merged)
}

// mergeMaps recursively merges patch into original following RFC 7386 rules.
func mergeMaps(original, patch map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	// Copy all from original
	for k, v := range original {
		result[k] = v
	}

	// Apply patch
	for k, patchValue := range patch {
		if patchValue == nil {
			// nil in patch means delete the key
			delete(result, k)
			continue
		}

		originalValue, exists := original[k]
		if !exists {
			// Key doesn't exist in original, add it
			result[k] = patchValue
			continue
		}

		// Both exist - check if both are maps
		originalMap, originalIsMap := originalValue.(map[string]interface{})
		patchMap, patchIsMap := patchValue.(map[string]interface{})

		if originalIsMap && patchIsMap {
			// Both are maps, recurse
			result[k] = mergeMaps(originalMap, patchMap)
		} else {
			// Replace with patch value
			result[k] = patchValue
		}
	}

	return result
}
