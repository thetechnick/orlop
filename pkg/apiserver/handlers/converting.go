package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/thetechnick/orlop/pkg/apiserver/conversion"
	"github.com/thetechnick/orlop/pkg/apiserver/schema"
	"github.com/thetechnick/orlop/pkg/apiserver/storage"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// ConvertingResourceHandler wraps a ResourceHandler and performs conversions
// between public and private API types.
type ConvertingResourceHandler struct {
	store          storage.ResourceStore
	processor      *schema.Processor
	converter      *conversion.Converter
	gvk            runtimeschema.GroupVersionKind
	resourceType   string
	newObjectFunc  func() runtime.Object // Public type constructor
	newListFunc    func() runtime.Object // Public list constructor
	privateNewFunc func() runtime.Object // Private type constructor
}

// NewConvertingResourceHandler creates a new converting resource handler.
func NewConvertingResourceHandler(
	store storage.ResourceStore,
	processor *schema.Processor,
	converter *conversion.Converter,
	gvk runtimeschema.GroupVersionKind,
	resourceType string,
	publicNewObjectFunc func() runtime.Object,
	publicNewListFunc func() runtime.Object,
	privateNewFunc func() runtime.Object,
) *ConvertingResourceHandler {
	return &ConvertingResourceHandler{
		store:          store,
		processor:      processor,
		converter:      converter,
		gvk:            gvk,
		resourceType:   resourceType,
		newObjectFunc:  publicNewObjectFunc,
		newListFunc:    publicNewListFunc,
		privateNewFunc: privateNewFunc,
	}
}

// Create handles POST requests to create a new resource.
func (h *ConvertingResourceHandler) Create(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")

	// Parse request body as map for schema processing
	var objMap map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&objMap); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	// Process object (prune, default, validate) using public schema
	if errs := h.processor.Process(objMap); len(errs) > 0 {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("validation failed: %v", errs.ToAggregate()))
		return
	}

	// Convert to public typed object
	objJSON, _ := json.Marshal(objMap)
	publicObj := h.newObjectFunc()
	if err := json.Unmarshal(objJSON, publicObj); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to unmarshal object: %v", err))
		return
	}

	// Set metadata on public object
	accessor, err := meta.Accessor(publicObj)
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

	// Set GVK on public object
	publicObj.GetObjectKind().SetGroupVersionKind(h.gvk)

	// Convert public to private (no existing object)
	privateObj, err := h.converter.PublicToPrivate(publicObj, h.privateNewFunc())
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to convert to private: %v", err))
		return
	}

	// Set GVK on private object for storage
	privateObj.GetObjectKind().SetGroupVersionKind(h.gvk)

	// Store private object
	if err := h.store.Create(namespace, name, privateObj); err != nil {
		if errors.IsAlreadyExists(err) {
			writeError(w, http.StatusConflict, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create object: %v", err))
		}
		return
	}

	// Convert stored private object back to public to get ResourceVersion
	responsePublic, err := h.converter.PrivateToPublic(privateObj)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to convert to public: %v", err))
		return
	}

	// Return public representation
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(responsePublic)
}

// Get handles GET requests to retrieve a single resource.
func (h *ConvertingResourceHandler) Get(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	// Get private object from storage
	privateObj, err := h.store.Get(namespace, name)
	if err != nil {
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get object: %v", err))
		}
		return
	}

	// Convert to public
	publicObj, err := h.converter.PrivateToPublic(privateObj)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to convert to public: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(publicObj)
}

// List handles GET requests to list resources.
func (h *ConvertingResourceHandler) List(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")

	// Parse label selector from query parameter
	opts := storage.ListOptions{}
	if labelSelector := r.URL.Query().Get("labelSelector"); labelSelector != "" {
		selector, err := labels.Parse(labelSelector)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid label selector: %v", err))
			return
		}
		opts.LabelSelector = selector
	}

	// Check if this is a watch request
	if r.URL.Query().Get("watch") == "true" {
		h.handleWatch(w, r, namespace, opts)
		return
	}

	// Get private objects from storage
	privateObjects, err := h.store.List(namespace, opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list objects: %v", err))
		return
	}

	// Convert each private object to public
	publicObjects := make([]runtime.Object, 0, len(privateObjects))
	for _, privateObj := range privateObjects {
		publicObj, err := h.converter.PrivateToPublic(privateObj)
		if err != nil {
			continue // Skip objects that fail conversion
		}
		publicObjects = append(publicObjects, publicObj)
	}

	// Create list object
	listMap := map[string]interface{}{
		"apiVersion": h.gvk.Group + "/" + h.gvk.Version,
		"kind":       h.gvk.Kind + "List",
		"metadata": map[string]interface{}{
			"resourceVersion": h.store.CurrentResourceVersion(),
		},
		"items": publicObjects,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(listMap)
}

// handleWatch handles watch requests using streaming JSON.
func (h *ConvertingResourceHandler) handleWatch(w http.ResponseWriter, r *http.Request, namespace string, opts storage.ListOptions) {
	// Get resourceVersion to start from
	resourceVersion := r.URL.Query().Get("resourceVersion")

	// Start watch
	eventCh, stop, err := h.store.Watch(namespace, opts, resourceVersion)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("failed to start watch: %v", err))
		return
	}
	defer stop()

	// Set headers for streaming
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.WriteHeader(http.StatusOK)

	// Get flusher for streaming
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

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

			// Convert private object to public
			publicObj, err := h.converter.PrivateToPublic(event.Object)
			if err != nil {
				continue // Skip objects that fail conversion
			}

			// Send watch event
			watchEvent := map[string]interface{}{
				"type":   event.Type,
				"object": publicObj,
			}

			if err := encoder.Encode(watchEvent); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// Update handles PUT requests to update a resource.
func (h *ConvertingResourceHandler) Update(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	// Get existing private object
	existingPrivate, err := h.store.Get(namespace, name)
	if err != nil {
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get existing object: %v", err))
		}
		return
	}

	// Parse request body
	var objMap map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&objMap); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	// Process object (prune, default, validate) using public schema
	if errs := h.processor.Process(objMap); len(errs) > 0 {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("validation failed: %v", errs.ToAggregate()))
		return
	}

	// Convert to public typed object
	objJSON, _ := json.Marshal(objMap)
	publicObj := h.newObjectFunc()
	if err := json.Unmarshal(objJSON, publicObj); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to unmarshal object: %v", err))
		return
	}

	// Set metadata
	accessor, err := meta.Accessor(publicObj)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to access metadata: %v", err))
		return
	}

	accessor.SetNamespace(namespace)
	accessor.SetName(name)

	// Convert public to private, preserving internal fields
	privateObj, err := h.converter.PublicToPrivate(publicObj, existingPrivate)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to convert to private: %v", err))
		return
	}

	// Check if spec changed and increment generation if so
	existingAccessor, _ := meta.Accessor(existingPrivate)
	privateAccessor, _ := meta.Accessor(privateObj)
	if specChanged(existingPrivate, privateObj) {
		privateAccessor.SetGeneration(existingAccessor.GetGeneration() + 1)
	} else {
		privateAccessor.SetGeneration(existingAccessor.GetGeneration())
	}

	// Set GVK
	privateObj.GetObjectKind().SetGroupVersionKind(h.gvk)

	// Update object in storage
	if err := h.store.Update(namespace, name, privateObj); err != nil {
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
		} else if errors.IsConflict(err) {
			writeError(w, http.StatusConflict, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to update object: %v", err))
		}
		return
	}

	// Convert back to public for response
	responsePublic, _ := h.converter.PrivateToPublic(privateObj)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(responsePublic)
}

// Patch handles PATCH requests to partially update a resource.
func (h *ConvertingResourceHandler) Patch(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	// Get existing private object
	existingPrivate, err := h.store.Get(namespace, name)
	if err != nil {
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get object: %v", err))
		}
		return
	}

	// Convert to public for patching
	existingPublic, err := h.converter.PrivateToPublic(existingPrivate)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to convert to public: %v", err))
		return
	}

	// Read patch body
	patchBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("failed to read patch: %v", err))
		return
	}

	// Convert existing public object to JSON
	existingJSON, err := json.Marshal(existingPublic)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to marshal existing object: %v", err))
		return
	}

	// Apply merge patch (same logic as ResourceHandler)
	patchedJSON, err := jsonMergePatch(existingJSON, patchBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("patch failed: %v", err))
		return
	}

	// Convert to map for schema processing
	var objMap map[string]interface{}
	if err := json.Unmarshal(patchedJSON, &objMap); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to unmarshal patched object: %v", err))
		return
	}

	// Process object (prune, default, validate) using public schema
	if errs := h.processor.Process(objMap); len(errs) > 0 {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("validation failed: %v", errs.ToAggregate()))
		return
	}

	// Convert to public typed object
	objJSON, _ := json.Marshal(objMap)
	publicObj := h.newObjectFunc()
	if err := json.Unmarshal(objJSON, publicObj); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to unmarshal object: %v", err))
		return
	}

	// Set metadata
	accessor, err := meta.Accessor(publicObj)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to access metadata: %v", err))
		return
	}

	accessor.SetNamespace(namespace)
	accessor.SetName(name)

	// Convert public to private
	privateObj, err := h.converter.PublicToPrivate(publicObj, existingPrivate)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to convert to private: %v", err))
		return
	}

	// Check if spec changed and increment generation if so
	existingAccessor, _ := meta.Accessor(existingPrivate)
	privateAccessor, _ := meta.Accessor(privateObj)
	if specChanged(existingPrivate, privateObj) {
		privateAccessor.SetGeneration(existingAccessor.GetGeneration() + 1)
	} else {
		privateAccessor.SetGeneration(existingAccessor.GetGeneration())
	}

	// Set GVK
	privateObj.GetObjectKind().SetGroupVersionKind(h.gvk)

	// Update object in storage
	if err := h.store.Update(namespace, name, privateObj); err != nil {
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
		} else if errors.IsConflict(err) {
			writeError(w, http.StatusConflict, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to update object: %v", err))
		}
		return
	}

	// Convert back to public for response
	responsePublic, _ := h.converter.PrivateToPublic(privateObj)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(responsePublic)
}

// Delete handles DELETE requests to delete a resource.
func (h *ConvertingResourceHandler) Delete(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if err := h.store.Delete(namespace, name); err != nil {
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to delete object: %v", err))
		}
		return
	}

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

// UpdateStatus handles PUT requests to update only the status subresource.
func (h *ConvertingResourceHandler) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	// Parse request body
	var updateMap map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updateMap); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	// Get existing private object
	existingPrivate, err := h.store.Get(namespace, name)
	if err != nil {
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get object: %v", err))
		}
		return
	}

	// Check resource version
	updateRV, _ := getResourceVersionFromMap(updateMap)
	existingAccessor, err := meta.Accessor(existingPrivate)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to access metadata: %v", err))
		return
	}
	existingRV := existingAccessor.GetResourceVersion()

	if updateRV != "" && updateRV != existingRV {
		writeError(w, http.StatusConflict, fmt.Sprintf("resource version mismatch: expected %s, got %s", existingRV, updateRV))
		return
	}

	// Convert existing private to map, update only status
	existingJSON, _ := json.Marshal(existingPrivate)
	var existingMap map[string]interface{}
	json.Unmarshal(existingJSON, &existingMap)

	// Replace only the status field
	if status, ok := updateMap["status"]; ok {
		existingMap["status"] = status
	}

	// Convert back to private object
	updatedJSON, _ := json.Marshal(existingMap)
	updatedPrivate := h.privateNewFunc()
	if err := json.Unmarshal(updatedJSON, updatedPrivate); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to unmarshal object: %v", err))
		return
	}

	// Set GVK
	updatedPrivate.GetObjectKind().SetGroupVersionKind(h.gvk)

	// Update in storage
	if err := h.store.Update(namespace, name, updatedPrivate); err != nil {
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
		} else if errors.IsConflict(err) {
			writeError(w, http.StatusConflict, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to update status: %v", err))
		}
		return
	}

	// Convert to public for response
	responsePublic, _ := h.converter.PrivateToPublic(updatedPrivate)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(responsePublic)
}
