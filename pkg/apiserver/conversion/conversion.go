package conversion

import (
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
)

// Converter handles conversion between private and public API types using scheme conversion.
type Converter struct {
	publicScheme  *runtime.Scheme
	privateScheme *runtime.Scheme
}

// NewConverter creates a new converter.
func NewConverter(publicScheme, privateScheme *runtime.Scheme) *Converter {
	return &Converter{
		publicScheme:  publicScheme,
		privateScheme: privateScheme,
	}
}

// PrivateToPublic converts a private API object to its public representation.
// Uses JSON round-trip for conversion since both types have the same GVK.
func (c *Converter) PrivateToPublic(private runtime.Object) (runtime.Object, error) {
	// Get the GVK from the private object
	gvk := private.GetObjectKind().GroupVersionKind()

	// Marshal private object to JSON
	jsonData, err := json.Marshal(private)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal private object: %w", err)
	}

	// Create a new public object of the same GVK
	public, err := c.publicScheme.New(gvk)
	if err != nil {
		return nil, fmt.Errorf("failed to create public object for %s: %w", gvk, err)
	}

	// Unmarshal into public object (JSON will only populate fields that exist in public type)
	if err := json.Unmarshal(jsonData, public); err != nil {
		return nil, fmt.Errorf("failed to unmarshal to public object: %w", err)
	}

	// Preserve GVK
	public.GetObjectKind().SetGroupVersionKind(gvk)

	return public, nil
}

// PublicToPrivate converts a public API object to its private representation.
// Uses JSON round-trip for conversion.
// The existing parameter can be used to preserve internal fields.
func (c *Converter) PublicToPrivate(public runtime.Object, existing runtime.Object) (runtime.Object, error) {
	// Get the GVK from the public object
	gvk := public.GetObjectKind().GroupVersionKind()

	// Marshal public object to JSON
	jsonData, err := json.Marshal(public)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal public object: %w", err)
	}

	// Create a new private object of the same GVK
	private, err := c.privateScheme.New(gvk)
	if err != nil {
		return nil, fmt.Errorf("failed to create private object for %s: %w", gvk, err)
	}

	// If existing object provided, start with it to preserve internal fields
	if existing != nil {
		// Marshal existing to get all fields
		existingJSON, err := json.Marshal(existing)
		if err == nil {
			// Unmarshal existing into private first
			json.Unmarshal(existingJSON, private)
		}
	}

	// Unmarshal public data into private (will overwrite public fields)
	if err := json.Unmarshal(jsonData, private); err != nil {
		return nil, fmt.Errorf("failed to unmarshal to private object: %w", err)
	}

	// Preserve GVK
	private.GetObjectKind().SetGroupVersionKind(gvk)

	return private, nil
}
