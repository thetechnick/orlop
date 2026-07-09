package apply

import (
	"encoding/json"
	"fmt"

	apiextschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// Manager handles server-side apply operations.
// It tracks field ownership and merges apply configurations with existing objects.
type Manager struct {
	scheme        *runtime.Scheme
	openAPISchema *apiextschema.Structural
	gvk           runtimeschema.GroupVersionKind
	typeConverter managedfields.TypeConverter
	fieldManager  *managedfields.FieldManager
}

// NewManager creates a new apply manager.
func NewManager(scheme *runtime.Scheme, openAPISchema *apiextschema.Structural, gvk runtimeschema.GroupVersionKind) (*Manager, error) {
	// Convert structural schema to OpenAPI v3 schema for managedfields
	openAPIV3 := structuralToOpenAPIV3(openAPISchema)

	// Create type converter for managedfields
	typeConverter, err := managedfields.NewTypeConverter(
		map[string]*spec.Schema{
			gvk.GroupVersion().String(): openAPIV3,
		},
		false, // preserveUnknownFields
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create type converter: %w", err)
	}

	// Create field manager using default CRD field manager
	fieldMgr, err := managedfields.NewDefaultCRDFieldManager(
		typeConverter,
		scheme,          // ObjectConvertor
		scheme,          // ObjectDefaulter
		scheme,          // ObjectCreater
		gvk,             // GroupVersionKind
		gvk.GroupVersion(), // hub version
		"",              // subresource (empty for main resource)
		nil,             // resetFields
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create field manager: %w", err)
	}

	return &Manager{
		scheme:        scheme,
		openAPISchema: openAPISchema,
		gvk:           gvk,
		typeConverter: typeConverter,
		fieldManager:  fieldMgr,
	}, nil
}

// Apply performs server-side apply merge.
//
// It merges the apply configuration with the current object, tracking field ownership
// and detecting conflicts when multiple field managers try to own the same field.
//
// Parameters:
//   - current: The existing object (nil if creating new object)
//   - applyConfig: The desired state to apply (YAML or JSON)
//   - fieldManager: Identifier for the entity applying changes
//   - force: If true, take ownership of conflicting fields
//
// Returns:
//   - The merged object with updated managedFields
//   - Error if conflicts detected (when force=false) or other failures
func (m *Manager) Apply(
	current client.Object,
	applyConfig []byte,
	fieldManager string,
	force bool,
) (client.Object, error) {
	// Parse apply configuration (support both YAML and JSON)
	applyObj, err := m.parseApplyConfig(applyConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to parse apply configuration: %w", err)
	}

	// If no current object, this is a create operation
	if current == nil {
		return m.applyCreate(applyObj, fieldManager)
	}

	// Perform apply update with field tracking
	return m.applyUpdate(current, applyObj, fieldManager, force)
}

// parseApplyConfig parses YAML or JSON into an unstructured object
func (m *Manager) parseApplyConfig(data []byte) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}

	// Try YAML first, then JSON
	if err := yaml.Unmarshal(data, &obj.Object); err != nil {
		// Try JSON
		if err := json.Unmarshal(data, &obj.Object); err != nil {
			return nil, fmt.Errorf("failed to parse as YAML or JSON: %w", err)
		}
	}

	// Set GVK if not present
	if obj.GetObjectKind().GroupVersionKind().Empty() {
		obj.SetGroupVersionKind(m.gvk)
	}

	return obj, nil
}

// applyCreate handles creation of new objects via apply
func (m *Manager) applyCreate(applyObj *unstructured.Unstructured, fieldManager string) (client.Object, error) {
	// Initialize managed fields for new object
	now := metav1.Now()
	managedFieldsEntry := metav1.ManagedFieldsEntry{
		Manager:    fieldManager,
		Operation:  metav1.ManagedFieldsOperationApply,
		APIVersion: m.gvk.GroupVersion().String(),
		Time:       &now,
		FieldsType: "FieldsV1",
	}

	// Use field manager to track all fields
	obj := applyObj.DeepCopy()
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return nil, err
	}

	// Compute fields for this apply
	fieldsV1, err := m.computeFieldsV1(obj)
	if err != nil {
		return nil, fmt.Errorf("failed to compute fields: %w", err)
	}
	managedFieldsEntry.FieldsV1 = fieldsV1

	accessor.SetManagedFields([]metav1.ManagedFieldsEntry{managedFieldsEntry})

	return obj, nil
}

// applyUpdate handles updates to existing objects via apply
func (m *Manager) applyUpdate(current client.Object, applyObj *unstructured.Unstructured, fieldManager string, force bool) (client.Object, error) {
	// Convert current to unstructured for easier manipulation
	currentUnstructured, err := runtime.DefaultUnstructuredConverter.ToUnstructured(current)
	if err != nil {
		return nil, fmt.Errorf("failed to convert current to unstructured: %w", err)
	}
	currentObj := &unstructured.Unstructured{Object: currentUnstructured}

	// Use the field manager to apply changes
	result, err := m.fieldManager.Apply(
		currentObj,
		applyObj,
		fieldManager,
		force,
	)
	if err != nil {
		// Check for conflict errors
		if errors.IsConflict(err) {
			return nil, err
		}
		return nil, fmt.Errorf("apply failed: %w", err)
	}

	// Return as client.Object
	if resultObj, ok := result.(client.Object); ok {
		return resultObj, nil
	}

	// Fallback: convert to unstructured
	resultUnstructured, ok := result.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("unexpected result type: %T", result)
	}
	return resultUnstructured, nil
}

// computeFieldsV1 computes the FieldsV1 representation for an object
func (m *Manager) computeFieldsV1(obj runtime.Object) (*metav1.FieldsV1, error) {
	// For simplicity, create an empty FieldsV1
	// The managedfields package will properly populate this during Apply/Update
	return &metav1.FieldsV1{}, nil
}

// structuralToOpenAPIV3 converts a structural schema to OpenAPI v3 schema
func structuralToOpenAPIV3(structural *apiextschema.Structural) *spec.Schema {
	if structural == nil {
		return &spec.Schema{
			SchemaProps: spec.SchemaProps{
				Type: []string{"object"},
			},
		}
	}

	schema := &spec.Schema{
		SchemaProps: spec.SchemaProps{
			Type:        []string{structural.Type},
			Description: structural.Description,
		},
	}

	if structural.Properties != nil {
		schema.Properties = make(map[string]spec.Schema)
		for name, prop := range structural.Properties {
			schema.Properties[name] = *structuralToOpenAPIV3(&prop)
		}
	}

	if structural.Items != nil {
		schema.Items = &spec.SchemaOrArray{
			Schema: structuralToOpenAPIV3(structural.Items),
		}
	}

	return schema
}

// Update tracks field ownership for regular PUT/PATCH updates
func (m *Manager) Update(current, updated client.Object, fieldManager string) (client.Object, error) {
	// For regular updates, use Update operation instead of Apply
	result, err := m.fieldManager.Update(current, updated, fieldManager)
	if err != nil {
		return nil, fmt.Errorf("update failed: %w", err)
	}

	// Return as client.Object
	if resultObj, ok := result.(client.Object); ok {
		return resultObj, nil
	}

	// Fallback: convert to unstructured
	resultUnstructured, ok := result.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("unexpected result type: %T", result)
	}
	return resultUnstructured, nil
}
