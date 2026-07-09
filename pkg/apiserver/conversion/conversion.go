package conversion

import (
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
// Uses the scheme's conversion functions which are auto-generated.
func (c *Converter) PrivateToPublic(private runtime.Object) (runtime.Object, error) {
	// Get the GVK from the private object
	gvk := private.GetObjectKind().GroupVersionKind()

	// Create a new public object of the same type
	public, err := c.publicScheme.New(gvk)
	if err != nil {
		return nil, fmt.Errorf("failed to create public object for %s: %w", gvk, err)
	}

	// Use scheme conversion (will use generated conversion functions)
	if err := c.publicScheme.Convert(private, public, nil); err != nil {
		return nil, fmt.Errorf("failed to convert private to public: %w", err)
	}

	// Preserve GVK
	public.GetObjectKind().SetGroupVersionKind(gvk)

	return public, nil
}

// PublicToPrivate converts a public API object to its private representation.
// Uses the scheme's conversion functions which are auto-generated.
// The existing parameter can be used to preserve internal fields, but for now we do a clean conversion.
func (c *Converter) PublicToPrivate(public runtime.Object, existing runtime.Object) (runtime.Object, error) {
	// Get the GVK from the public object
	gvk := public.GetObjectKind().GroupVersionKind()

	// Create a new private object of the same type
	private, err := c.privateScheme.New(gvk)
	if err != nil {
		return nil, fmt.Errorf("failed to create private object for %s: %w", gvk, err)
	}

	// Use scheme conversion (will use generated conversion functions)
	if err := c.privateScheme.Convert(public, private, nil); err != nil {
		return nil, fmt.Errorf("failed to convert public to private: %w", err)
	}

	// Preserve GVK
	private.GetObjectKind().SetGroupVersionKind(gvk)

	// TODO: Preserve internal fields from existing object if provided
	// This would require copying specific fields after conversion

	return private, nil
}
