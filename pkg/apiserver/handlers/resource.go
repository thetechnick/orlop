package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-logr/logr"
	"github.com/google/uuid"
	"github.com/thetechnick/orlop/pkg/apiserver/apply"
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
	applyManager *apply.Manager // Optional: for server-side apply support
	logger       logr.Logger
}

// NewResourceHandler creates a new resource handler.
func NewResourceHandler(
	store storage.ResourceStore,
	processor *schema.Processor,
	gvk runtimeschema.GroupVersionKind,
	resourceType string,
	scheme *runtime.Scheme,
	logger logr.Logger,
) *ResourceHandler {
	if logger.GetSink() == nil {
		logger = logr.Discard()
	}
	return &ResourceHandler{
		store:        store,
		processor:    processor,
		gvk:          gvk,
		resourceType: resourceType,
		scheme:       scheme,
		applyManager: nil, // Will be set by SetApplyManager if SSA is enabled
		logger:       logger,
	}
}

// SetApplyManager sets the apply manager for server-side apply support.
func (h *ResourceHandler) SetApplyManager(applyMgr *apply.Manager) {
	h.applyManager = applyMgr
}

// Create handles POST requests to create a new resource.
func (h *ResourceHandler) Create(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	h.logger.V(1).Info("Create request", "kind", h.gvk.Kind, "namespace", namespace)

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
		h.logger.Error(err, "Create failed", "kind", h.gvk.Kind, "namespace", namespace, "name", name)
		if errors.IsAlreadyExists(err) {
			writeError(w, http.StatusConflict, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create object: %v", err))
		}
		return
	}

	h.logger.Info("Created", "kind", h.gvk.Kind, "namespace", namespace, "name", name)

	// Return created object
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(clientObj)
}

// Get handles GET requests to retrieve a single resource.
func (h *ResourceHandler) Get(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	h.logger.V(1).Info("Get request", "kind", h.gvk.Kind, "namespace", namespace, "name", name)

	obj, err := h.store.Get(namespace, name)
	if err != nil {
		h.logger.Error(err, "Get failed", "kind", h.gvk.Kind, "namespace", namespace, "name", name)
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get object: %v", err))
		}
		return
	}

	h.logger.V(1).Info("Found", "kind", h.gvk.Kind, "namespace", namespace, "name", name)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(obj)
}

// List handles GET requests to list resources.
func (h *ResourceHandler) List(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	if namespace == "" {
		h.logger.V(1).Info("List request", "kind", h.gvk.Kind, "scope", "cluster")
	} else {
		h.logger.V(1).Info("List request", "kind", h.gvk.Kind, "namespace", namespace)
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
				h.logger.Info("Invalid label selector", "kind", h.gvk.Kind, "scope", "cluster")
			} else {
				h.logger.Info("Invalid label selector", "kind", h.gvk.Kind, "namespace", namespace)
			}
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid label selector: %v", err))
			return
		}
		opts.LabelSelector = selector
	}

	// Check if this is a watch request
	if r.URL.Query().Get("watch") == "true" {
		if namespace == "" {
			h.logger.V(1).Info("Watch request", "kind", h.gvk.Kind, "scope", "cluster", "uri", r.RequestURI)
		} else {
			h.logger.V(1).Info("Watch request", "kind", h.gvk.Kind, "namespace", namespace, "uri", r.RequestURI)
		}
		h.handleWatch(w, r, opts)
		return
	}

	list, err := h.store.List(opts)
	if err != nil {
		if namespace == "" {
			h.logger.Error(err, "List failed", "kind", h.gvk.Kind, "scope", "cluster")
		} else {
			h.logger.Error(err, "List failed", "kind", h.gvk.Kind, "namespace", namespace)
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
		h.logger.V(1).Info("Listed", "kind", h.gvk.Kind, "scope", "cluster", "count", count)
	} else {
		h.logger.V(1).Info("Listed", "kind", h.gvk.Kind, "namespace", namespace, "count", count)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(list)
}

// handleWatch handles watch requests using Server-Sent Events.

// Update handles PUT requests to update a resource.
func (h *ResourceHandler) Update(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	h.logger.V(1).Info("Update request", "kind", h.gvk.Kind, "namespace", namespace, "name", name)

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
		h.logger.Error(err, "Update failed", "kind", h.gvk.Kind, "namespace", namespace, "name", name)
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
		} else if errors.IsConflict(err) {
			writeError(w, http.StatusConflict, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to update object: %v", err))
		}
		return
	}

	h.logger.Info("Updated", "kind", h.gvk.Kind, "namespace", namespace, "name", name)

	// Return updated object
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(clientObj)
}

// Patch handles PATCH requests to partially update a resource.

// ApplyPatch handles server-side apply PATCH requests.
// This implements the Kubernetes server-side apply protocol with field ownership tracking.
func (h *ResourceHandler) ApplyPatch(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	h.logger.V(1).Info("Apply request", "kind", h.gvk.Kind, "namespace", namespace, "name", name)

	// Check if apply manager is available
	if h.applyManager == nil {
		writeError(w, http.StatusNotImplemented, "Server-side apply is not enabled for this resource")
		return
	}

	// Extract field manager from query parameters (required)
	fieldManager := r.URL.Query().Get("fieldManager")
	if fieldManager == "" {
		writeError(w, http.StatusBadRequest, "fieldManager query parameter is required for server-side apply")
		return
	}

	// Extract force parameter (optional, defaults to false)
	force := r.URL.Query().Get("force") == "true"

	// Read apply configuration body
	applyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("failed to read request body: %v", err))
		return
	}

	// Get existing object (if it exists)
	existing, err := h.store.Get(namespace, name)
	if err != nil && !errors.IsNotFound(err) {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get object: %v", err))
		return
	}

	// Perform server-side apply
	result, err := h.applyManager.Apply(existing, applyBytes, fieldManager, force)
	if err != nil {
		if errors.IsConflict(err) {
			writeError(w, http.StatusConflict, fmt.Sprintf("Apply conflict: %v", err))
		} else {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("Apply failed: %v", err))
		}
		return
	}

	// Ensure namespace is set
	accessor, err := meta.Accessor(result)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to access metadata: %v", err))
		return
	}
	accessor.SetNamespace(namespace)

	// Save to storage (create or update)
	if existing == nil {
		// This is a create via apply
		accessor.SetUID(types.UID(uuid.New().String()))
		accessor.SetCreationTimestamp(metav1.Time{Time: time.Now()})
		accessor.SetGeneration(1)

		if err := h.store.Create(result); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create object: %v", err))
			return
		}

		// Return created object
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(result)
		h.logger.Info("Created via apply", "namespace", namespace, "name", name, "fieldManager", fieldManager)
	} else {
		// This is an update via apply
		if err := h.store.Update(result); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to update object: %v", err))
			return
		}

		// Return updated object
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(result)
		h.logger.Info("Updated via apply", "namespace", namespace, "name", name, "fieldManager", fieldManager, "force", force)
	}
}

// Delete handles DELETE requests to delete a resource.
func (h *ResourceHandler) Delete(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	h.logger.V(1).Info("Delete request", "kind", h.gvk.Kind, "namespace", namespace, "name", name)

	if err := h.store.Delete(namespace, name); err != nil {
		h.logger.Error(err, "Delete failed", "kind", h.gvk.Kind, "namespace", namespace, "name", name)
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to delete object: %v", err))
		}
		return
	}

	h.logger.Info("Deleted", "kind", h.gvk.Kind, "namespace", namespace, "name", name)

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
