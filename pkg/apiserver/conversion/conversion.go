package conversion

import (
	"encoding/json"

	privatev1 "github.com/thetechnick/orlop/apis/private/test/v1"
	publicv1 "github.com/thetechnick/orlop/apis/public/test/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Converter handles conversion between private and public API types.
type Converter struct{}

// NewConverter creates a new converter.
func NewConverter() *Converter {
	return &Converter{}
}

// PrivateToPublic converts a private API object to its public representation.
// This strips out internal fields that should not be exposed.
func (c *Converter) PrivateToPublic(private runtime.Object) (runtime.Object, error) {
	switch obj := private.(type) {
	case *privatev1.Object:
		return c.convertPrivateObjectToPublic(obj), nil
	case *privatev1.ObjectList:
		return c.convertPrivateObjectListToPublic(obj), nil
	case *privatev1.Other:
		return c.convertPrivateOtherToPublic(obj), nil
	case *privatev1.OtherList:
		return c.convertPrivateOtherListToPublic(obj), nil
	default:
		// Fallback: use JSON round-trip
		return c.jsonConvert(private, func() runtime.Object {
			// Return appropriate type based on GVK
			return private
		})
	}
}

// PublicToPrivate converts a public API object to its private representation.
// This preserves existing internal fields from storage.
func (c *Converter) PublicToPrivate(public runtime.Object, existing runtime.Object) (runtime.Object, error) {
	switch obj := public.(type) {
	case *publicv1.Object:
		return c.convertPublicObjectToPrivate(obj, existing.(*privatev1.Object)), nil
	case *publicv1.ObjectList:
		return c.convertPublicObjectListToPrivate(obj), nil
	case *publicv1.Other:
		return c.convertPublicOtherToPrivate(obj, existing.(*privatev1.Other)), nil
	case *publicv1.OtherList:
		return c.convertPublicOtherListToPrivate(obj), nil
	default:
		// Fallback: use JSON round-trip
		return c.jsonConvert(public, func() runtime.Object {
			return public
		})
	}
}

// Object conversions

func (c *Converter) convertPrivateObjectToPublic(in *privatev1.Object) *publicv1.Object {
	return &publicv1.Object{
		TypeMeta:   in.TypeMeta,
		ObjectMeta: in.ObjectMeta,
		Spec: publicv1.ObjectSpec{
			PublicField: in.Spec.PublicField,
			Nested: publicv1.ObjectNested{
				PublicField: in.Spec.Nested.PublicField,
			},
			DefaultField: in.Spec.DefaultField,
		},
		Status: publicv1.ObjectStatus{
			Conditions: in.Status.Conditions,
		},
	}
}

func (c *Converter) convertPublicObjectToPrivate(in *publicv1.Object, existing *privatev1.Object) *privatev1.Object {
	out := &privatev1.Object{
		TypeMeta:   in.TypeMeta,
		ObjectMeta: in.ObjectMeta,
		Spec: privatev1.ObjectSpec{
			PublicField: in.Spec.PublicField,
			Nested: privatev1.ObjectNested{
				PublicField: in.Spec.Nested.PublicField,
			},
			DefaultField: in.Spec.DefaultField,
		},
		Status: privatev1.ObjectStatus{
			Conditions: in.Status.Conditions,
		},
	}

	// Preserve internal fields from existing object
	if existing != nil {
		out.Spec.InternalField = existing.Spec.InternalField
		out.Spec.Nested.InternalField = existing.Spec.Nested.InternalField
	}

	return out
}

func (c *Converter) convertPrivateObjectListToPublic(in *privatev1.ObjectList) *publicv1.ObjectList {
	out := &publicv1.ObjectList{
		TypeMeta: in.TypeMeta,
		ListMeta: in.ListMeta,
		Items:    make([]publicv1.Object, len(in.Items)),
	}

	for i, item := range in.Items {
		publicObj := c.convertPrivateObjectToPublic(&item)
		out.Items[i] = *publicObj
	}

	return out
}

func (c *Converter) convertPublicObjectListToPrivate(in *publicv1.ObjectList) *privatev1.ObjectList {
	out := &privatev1.ObjectList{
		TypeMeta: in.TypeMeta,
		ListMeta: in.ListMeta,
		Items:    make([]privatev1.Object, len(in.Items)),
	}

	for i, item := range in.Items {
		privateObj := c.convertPublicObjectToPrivate(&item, nil)
		out.Items[i] = *privateObj
	}

	return out
}

// Other conversions

func (c *Converter) convertPrivateOtherToPublic(in *privatev1.Other) *publicv1.Other {
	return &publicv1.Other{
		TypeMeta:   in.TypeMeta,
		ObjectMeta: in.ObjectMeta,
		Spec: publicv1.OtherSpec{
			PublicField: in.Spec.PublicField,
		},
	}
}

func (c *Converter) convertPublicOtherToPrivate(in *publicv1.Other, existing *privatev1.Other) *privatev1.Other {
	out := &privatev1.Other{
		TypeMeta:   in.TypeMeta,
		ObjectMeta: in.ObjectMeta,
		Spec: privatev1.OtherSpec{
			PublicField: in.Spec.PublicField,
		},
	}

	// Preserve internal field and status from existing object
	if existing != nil {
		out.Spec.InternalField = existing.Spec.InternalField
		out.Status = existing.Status
	}

	return out
}

func (c *Converter) convertPrivateOtherListToPublic(in *privatev1.OtherList) *publicv1.OtherList {
	out := &publicv1.OtherList{
		TypeMeta: in.TypeMeta,
		ListMeta: in.ListMeta,
		Items:    make([]publicv1.Other, len(in.Items)),
	}

	for i, item := range in.Items {
		publicObj := c.convertPrivateOtherToPublic(&item)
		out.Items[i] = *publicObj
	}

	return out
}

func (c *Converter) convertPublicOtherListToPrivate(in *publicv1.OtherList) *privatev1.OtherList {
	out := &privatev1.OtherList{
		TypeMeta: in.TypeMeta,
		ListMeta: in.ListMeta,
		Items:    make([]privatev1.Other, len(in.Items)),
	}

	for i, item := range in.Items {
		privateObj := c.convertPublicOtherToPrivate(&item, nil)
		out.Items[i] = *privateObj
	}

	return out
}

// jsonConvert is a fallback conversion using JSON serialization.
func (c *Converter) jsonConvert(in runtime.Object, newFunc func() runtime.Object) (runtime.Object, error) {
	data, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}

	out := newFunc()
	if err := json.Unmarshal(data, out); err != nil {
		return nil, err
	}

	return out, nil
}
