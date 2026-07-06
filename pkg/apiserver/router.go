package apiserver

import (
	"fmt"

	"github.com/go-chi/chi/v5"
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

	// Group resources by GroupVersion
	gvResources := make(map[string][]ResourceInfo)
	for _, res := range registry.Resources() {
		gv := fmt.Sprintf("%s/%s", res.GVK.Group, res.GVK.Version)
		gvResources[gv] = append(gvResources[gv], res)
	}

	// Setup routes for each GroupVersion
	for gv, resources := range gvResources {
		apiPath := "/apis/" + gv

		r.Route(apiPath, func(r chi.Router) {
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
	}

	return r, nil
}
