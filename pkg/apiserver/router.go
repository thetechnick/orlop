package apiserver

import (
	"github.com/go-chi/chi/v5"
	testv1 "github.com/thetechnick/orlop/apis/private/test/v1"
	"github.com/thetechnick/orlop/pkg/apiserver/handlers"
	"github.com/thetechnick/orlop/pkg/apiserver/middleware"
	pkgschema "github.com/thetechnick/orlop/pkg/apiserver/schema"
	"github.com/thetechnick/orlop/pkg/apiserver/storage"
	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/runtime"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// setupRouter configures the HTTP router with all endpoints.
func setupRouter(store storage.ResourceStore, corsOrigins []string) (chi.Router, error) {
	r := chi.NewRouter()

	// Add CORS middleware
	r.Use(middleware.CORS(middleware.CORSOptions{
		AllowedOrigins: corsOrigins,
	}))

	// Create schema processors
	objectProcessor, err := createProcessor(testv1.ObjectSchemaYAML)
	if err != nil {
		return nil, err
	}

	otherProcessor, err := createProcessor(testv1.OtherSchemaYAML)
	if err != nil {
		return nil, err
	}

	// Create handlers
	objectHandler := handlers.NewResourceHandler(
		store,
		objectProcessor,
		runtimeschema.GroupVersionKind{
			Group:   "test.orlop.thetechnick.ninja",
			Version: "v1",
			Kind:    "Object",
		},
		"objects",
		func() runtime.Object { return &testv1.Object{} },
		func() runtime.Object { return &testv1.ObjectList{} },
	)

	otherHandler := handlers.NewResourceHandler(
		store,
		otherProcessor,
		runtimeschema.GroupVersionKind{
			Group:   "test.orlop.thetechnick.ninja",
			Version: "v1",
			Kind:    "Other",
		},
		"others",
		func() runtime.Object { return &testv1.Other{} },
		func() runtime.Object { return &testv1.OtherList{} },
	)

	// Setup routes
	r.Route("/apis/test.orlop.thetechnick.ninja/v1", func(r chi.Router) {
		r.Route("/namespaces/{namespace}", func(r chi.Router) {
			// Object resource
			r.Post("/objects", objectHandler.Create)
			r.Get("/objects", objectHandler.List)
			r.Get("/objects/{name}", objectHandler.Get)
			r.Put("/objects/{name}", objectHandler.Update)
			r.Delete("/objects/{name}", objectHandler.Delete)
			r.Put("/objects/{name}/status", objectHandler.UpdateStatus)

			// Other resource
			r.Post("/others", otherHandler.Create)
			r.Get("/others", otherHandler.List)
			r.Get("/others/{name}", otherHandler.Get)
			r.Put("/others/{name}", otherHandler.Update)
			r.Delete("/others/{name}", otherHandler.Delete)
			r.Put("/others/{name}/status", otherHandler.UpdateStatus)
		})
	})

	return r, nil
}

// createProcessor creates a schema processor from YAML schema.
func createProcessor(schemaYAML string) (*pkgschema.Processor, error) {
	// Parse YAML to v1 JSONSchemaProps
	var propsV1 apiextv1.JSONSchemaProps
	if err := yaml.Unmarshal([]byte(schemaYAML), &propsV1); err != nil {
		return nil, err
	}

	// Convert to internal JSONSchemaProps
	var props apiext.JSONSchemaProps
	if err := apiextv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(&propsV1, &props, nil); err != nil {
		return nil, err
	}

	// Create structural schema
	structural, err := schema.NewStructural(&props)
	if err != nil {
		return nil, err
	}

	// Create processor
	return pkgschema.NewProcessor(structural, &props)
}
