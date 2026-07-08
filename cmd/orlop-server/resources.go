package main

import (
	privatev1 "github.com/thetechnick/orlop/apis/private/test/v1"
	publicv1 "github.com/thetechnick/orlop/apis/public/test/v1"
	"github.com/thetechnick/orlop/pkg/apiserver"
	"k8s.io/apimachinery/pkg/runtime"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
)

// getPrivateResources returns the resource definitions for the private API.
func getPrivateResources() []apiserver.ResourceInfo {
	return []apiserver.ResourceInfo{
		{
			GVK: runtimeschema.GroupVersionKind{
				Group:   "test.orlop.thetechnick.ninja",
				Version: "v1",
				Kind:    "Object",
			},
			Plural:        privatev1.ObjectPlural,
			SchemaYAML:    privatev1.ObjectSchemaYAML,
			NewObjectFunc: func() runtime.Object { return &privatev1.Object{} },
			NewListFunc:   func() runtime.Object { return &privatev1.ObjectList{} },
		},
		{
			GVK: runtimeschema.GroupVersionKind{
				Group:   "test.orlop.thetechnick.ninja",
				Version: "v1",
				Kind:    "Other",
			},
			Plural:        privatev1.OtherPlural,
			SchemaYAML:    privatev1.OtherSchemaYAML,
			NewObjectFunc: func() runtime.Object { return &privatev1.Other{} },
			NewListFunc:   func() runtime.Object { return &privatev1.OtherList{} },
		},
	}
}

// getPublicResources returns the resource definitions for the public API.
func getPublicResources() []apiserver.ResourceInfo {
	return []apiserver.ResourceInfo{
		{
			GVK: runtimeschema.GroupVersionKind{
				Group:   "test.orlop.thetechnick.ninja",
				Version: "v1",
				Kind:    "Object",
			},
			Plural:         publicv1.ObjectPlural,
			SchemaYAML:     publicv1.ObjectSchemaYAML,
			NewObjectFunc:  func() runtime.Object { return &publicv1.Object{} },
			NewListFunc:    func() runtime.Object { return &publicv1.ObjectList{} },
			PrivateNewFunc: func() runtime.Object { return &privatev1.Object{} },
		},
		{
			GVK: runtimeschema.GroupVersionKind{
				Group:   "test.orlop.thetechnick.ninja",
				Version: "v1",
				Kind:    "Other",
			},
			Plural:         publicv1.OtherPlural,
			SchemaYAML:     publicv1.OtherSchemaYAML,
			NewObjectFunc:  func() runtime.Object { return &publicv1.Other{} },
			NewListFunc:    func() runtime.Object { return &publicv1.OtherList{} },
			PrivateNewFunc: func() runtime.Object { return &privatev1.Other{} },
		},
	}
}

// getScheme creates and returns a runtime.Scheme with the test types registered.
func getScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	privatev1.AddToScheme(scheme)
	return scheme
}
