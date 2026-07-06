package apiserver

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/thetechnick/orlop/pkg/apiserver/conversion"
	"github.com/thetechnick/orlop/pkg/apiserver/handlers"
	"github.com/thetechnick/orlop/pkg/apiserver/middleware"
	"github.com/thetechnick/orlop/pkg/apiserver/storage"
)

// setupRouter configures the HTTP router with all endpoints.
func setupRouter(store storage.ResourceStore, registry *ResourceRegistry, corsOrigins []string) (chi.Router, error) {
	r := chi.NewRouter()

	// Add CORS middleware
	r.Use(middleware.CORS(middleware.CORSOptions{
		AllowedOrigins: corsOrigins,
	}))

	// Create discovery handler
	discoveryHandler := handlers.NewDiscoveryHandler(registry)

	// Discovery endpoints (must be registered BEFORE resource routes to avoid shadowing)
	r.Get("/apis", discoveryHandler.APIGroupList)
	r.Get("/openapi/v3", discoveryHandler.OpenAPIV3)

	// Group resources by GroupVersion
	gvResources := make(map[string][]ResourceInfo)
	for _, res := range registry.GetResources() {
		gv := fmt.Sprintf("%s/%s", res.GVK.Group, res.GVK.Version)
		gvResources[gv] = append(gvResources[gv], res)
	}

	// Setup routes for each GroupVersion
	for gv, resources := range gvResources {
		group := resources[0].GVK.Group
		version := resources[0].GVK.Version
		apiPath := "/apis/" + gv

		r.Route(apiPath, func(r chi.Router) {
			// Discovery endpoint for this specific group/version (before namespaced routes)
			r.Get("/", func(w http.ResponseWriter, req *http.Request) {
				discoveryHandler.APIResourceList(w, req, group, version)
			})

			r.Route("/namespaces/{namespace}", func(r chi.Router) {
				// Register routes for each resource
				for _, res := range resources {
					handler, err := registry.CreateHandler(store, res)
					if err != nil {
						// Log error but continue with other resources
						continue
					}

					plural := res.Plural

					// CRUD endpoints
					r.Post("/"+plural, handler.Create)
					r.Get("/"+plural, handler.List)
					r.Get("/"+plural+"/{name}", handler.Get)
					r.Put("/"+plural+"/{name}", handler.Update)
					r.Delete("/"+plural+"/{name}", handler.Delete)

					// Status subresource
					r.Put("/"+plural+"/{name}/status", handler.UpdateStatus)
				}
			})
		})

		// Per-group discovery endpoint
		r.Get("/apis/"+group, func(w http.ResponseWriter, req *http.Request) {
			discoveryHandler.APIGroup(w, req, group)
		})

		// OpenAPI v3 per-group-version endpoint
		r.Get("/openapi/v3/apis/"+gv, func(w http.ResponseWriter, req *http.Request) {
			discoveryHandler.OpenAPIV3GroupVersion(w, req, group, version)
		})
	}

	return r, nil
}

// setupConvertingRouter configures the HTTP router with converting handlers for public API.
func setupConvertingRouter(store storage.ResourceStore, registry *ResourceRegistry, converter *conversion.Converter, corsOrigins []string) (chi.Router, error) {
	r := chi.NewRouter()

	// Add CORS middleware
	r.Use(middleware.CORS(middleware.CORSOptions{
		AllowedOrigins: corsOrigins,
	}))

	// Create discovery handler
	discoveryHandler := handlers.NewDiscoveryHandler(registry)

	// Discovery endpoints (must be registered BEFORE resource routes to avoid shadowing)
	r.Get("/apis", discoveryHandler.APIGroupList)
	r.Get("/openapi/v3", discoveryHandler.OpenAPIV3)

	// Group resources by GroupVersion
	gvResources := make(map[string][]ResourceInfo)
	for _, res := range registry.GetResources() {
		gv := fmt.Sprintf("%s/%s", res.GVK.Group, res.GVK.Version)
		gvResources[gv] = append(gvResources[gv], res)
	}

	// Setup routes for each GroupVersion
	for gv, resources := range gvResources {
		group := resources[0].GVK.Group
		version := resources[0].GVK.Version
		apiPath := "/apis/" + gv

		r.Route(apiPath, func(r chi.Router) {
			// Discovery endpoint for this specific group/version (before namespaced routes)
			r.Get("/", func(w http.ResponseWriter, req *http.Request) {
				discoveryHandler.APIResourceList(w, req, group, version)
			})

			r.Route("/namespaces/{namespace}", func(r chi.Router) {
				// Register routes for each resource
				for _, res := range resources {
					handlerInterface, err := registry.CreateConvertingHandler(store, converter, res)
					if err != nil {
						// Log error but continue with other resources
						continue
					}

					handler := handlerInterface.(*handlers.ConvertingResourceHandler)
					plural := res.Plural

					// CRUD endpoints
					r.Post("/"+plural, handler.Create)
					r.Get("/"+plural, handler.List)
					r.Get("/"+plural+"/{name}", handler.Get)
					r.Put("/"+plural+"/{name}", handler.Update)
					r.Delete("/"+plural+"/{name}", handler.Delete)

					// Status subresource
					r.Put("/"+plural+"/{name}/status", handler.UpdateStatus)
				}
			})
		})

		// Per-group discovery endpoint
		r.Get("/apis/"+group, func(w http.ResponseWriter, req *http.Request) {
			discoveryHandler.APIGroup(w, req, group)
		})

		// OpenAPI v3 per-group-version endpoint
		r.Get("/openapi/v3/apis/"+gv, func(w http.ResponseWriter, req *http.Request) {
			discoveryHandler.OpenAPIV3GroupVersion(w, req, group, version)
		})
	}

	return r, nil
}
