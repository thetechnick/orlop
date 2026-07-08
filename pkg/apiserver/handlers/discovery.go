package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// ResourceInfo describes a single API resource type (duplicated to avoid import cycle).
type ResourceInfo struct {
	GVK            runtimeschema.GroupVersionKind
	Plural         string
	SchemaYAML     string
	NewObjectFunc  func() runtime.Object
	NewListFunc    func() runtime.Object
	PrivateNewFunc func() runtime.Object
}

// ResourceProvider provides access to registered resources.
type ResourceProvider interface {
	Resources() []ResourceInfo
}

// DiscoveryHandler handles API discovery requests.
type DiscoveryHandler struct {
	resources []ResourceInfo
}

// NewDiscoveryHandler creates a new discovery handler.
func NewDiscoveryHandler(provider ResourceProvider) *DiscoveryHandler {
	return &DiscoveryHandler{
		resources: provider.Resources(),
	}
}

// APIGroupList handles GET /apis
// Returns the list of API groups available.
func (h *DiscoveryHandler) APIGroupList(w http.ResponseWriter, r *http.Request) {
	groups := make(map[string]*metav1.APIGroup)

	// Collect unique groups and their versions
	for _, res := range h.resources {
		group := res.GVK.Group
		version := res.GVK.Version

		if _, exists := groups[group]; !exists {
			groups[group] = &metav1.APIGroup{
				TypeMeta: metav1.TypeMeta{
					Kind:       "APIGroup",
					APIVersion: "v1",
				},
				Name:     group,
				Versions: []metav1.GroupVersionForDiscovery{},
			}
		}

		// Add version if not already present
		versionExists := false
		for _, v := range groups[group].Versions {
			if v.Version == version {
				versionExists = true
				break
			}
		}

		if !versionExists {
			groups[group].Versions = append(groups[group].Versions, metav1.GroupVersionForDiscovery{
				GroupVersion: group + "/" + version,
				Version:      version,
			})
		}

		// Set preferred version (first one)
		if len(groups[group].Versions) == 1 {
			groups[group].PreferredVersion = groups[group].Versions[0]
		}
	}

	// Convert map to list
	groupList := &metav1.APIGroupList{
		TypeMeta: metav1.TypeMeta{
			Kind:       "APIGroupList",
			APIVersion: "v1",
		},
		Groups: make([]metav1.APIGroup, 0, len(groups)),
	}

	for _, group := range groups {
		groupList.Groups = append(groupList.Groups, *group)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(groupList)
}

// APIGroup handles GET /apis/{group}
// Returns the list of versions for a specific API group.
func (h *DiscoveryHandler) APIGroup(w http.ResponseWriter, r *http.Request, group string) {
	apiGroup := &metav1.APIGroup{
		TypeMeta: metav1.TypeMeta{
			Kind:       "APIGroup",
			APIVersion: "v1",
		},
		Name:     group,
		Versions: []metav1.GroupVersionForDiscovery{},
	}

	// Collect versions for this group
	versions := make(map[string]bool)
	for _, res := range h.resources {
		if res.GVK.Group == group {
			version := res.GVK.Version
			if !versions[version] {
				versions[version] = true
				apiGroup.Versions = append(apiGroup.Versions, metav1.GroupVersionForDiscovery{
					GroupVersion: group + "/" + version,
					Version:      version,
				})
			}
		}
	}

	if len(apiGroup.Versions) == 0 {
		writeError(w, http.StatusNotFound, "group not found")
		return
	}

	// Set preferred version (first one)
	apiGroup.PreferredVersion = apiGroup.Versions[0]

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(apiGroup)
}

// APIResourceList handles GET /apis/{group}/{version}
// Returns the list of resources for a specific API group version.
func (h *DiscoveryHandler) APIResourceList(w http.ResponseWriter, r *http.Request, group, version string) {
	resourceList := &metav1.APIResourceList{
		TypeMeta: metav1.TypeMeta{
			Kind:       "APIResourceList",
			APIVersion: "v1",
		},
		GroupVersion: group + "/" + version,
		APIResources: []metav1.APIResource{},
	}

	// Find resources for this group/version
	for _, res := range h.resources {
		if res.GVK.Group == group && res.GVK.Version == version {
			resource := metav1.APIResource{
				Name:       res.Plural,
				Kind:       res.GVK.Kind,
				Namespaced: true,
				Verbs:      metav1.Verbs{"create", "delete", "get", "list", "patch", "update", "watch"},
			}

			// Add main resource
			resourceList.APIResources = append(resourceList.APIResources, resource)

			// Add status subresource
			statusResource := metav1.APIResource{
				Name:       res.Plural + "/status",
				Kind:       res.GVK.Kind,
				Namespaced: true,
				Verbs:      metav1.Verbs{"get", "patch", "update"},
			}
			resourceList.APIResources = append(resourceList.APIResources, statusResource)
		}
	}

	if len(resourceList.APIResources) == 0 {
		writeError(w, http.StatusNotFound, "group version not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resourceList)
}

// OpenAPIV3 handles GET /openapi/v3
// Returns the list of available OpenAPI v3 group versions.
func (h *DiscoveryHandler) OpenAPIV3(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("[DEBUG] OpenAPIV3 request: %s %s (Accept: %s)\n", r.Method, r.URL.Path, r.Header.Get("Accept"))

	// Build a list of group versions
	groupVersions := make(map[string]bool)
	for _, res := range h.resources {
		gv := res.GVK.Group + "/" + res.GVK.Version
		groupVersions[gv] = true
	}

	// Convert to OpenAPI v3 discovery format
	paths := make(map[string]interface{})
	for gv := range groupVersions {
		paths["apis/"+gv] = map[string]interface{}{
			"serverRelativeURL": "/openapi/v3/apis/" + gv,
		}
	}

	response := map[string]interface{}{
		"paths": paths,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// OpenAPIV3GroupVersion handles GET /openapi/v3/apis/{group}/{version}
// Returns the OpenAPI v3 schema for a specific group version.
func (h *DiscoveryHandler) OpenAPIV3GroupVersion(w http.ResponseWriter, r *http.Request, group, version string) {
	fmt.Printf("[DEBUG] OpenAPIV3GroupVersion request: %s %s (Accept: %s, group=%s, version=%s)\n", r.Method, r.URL.Path, r.Header.Get("Accept"), group, version)
	gv := runtimeschema.GroupVersion{Group: group, Version: version}

	// Build OpenAPI v3 document
	spec := map[string]interface{}{
		"openapi": "3.0.0",
		"info": map[string]interface{}{
			"title":   group + "/" + version,
			"version": version,
		},
		"paths":      map[string]interface{}{},
		"components": map[string]interface{}{},
	}

	schemas := make(map[string]interface{})

	// Add each resource schema
	for _, res := range h.resources {
		if res.GVK.Group != group || res.GVK.Version != version {
			continue
		}

		// Parse the schema YAML to get the JSON schema
		var schemaObj map[string]interface{}
		if err := yaml.Unmarshal([]byte(res.SchemaYAML), &schemaObj); err != nil {
			// Skip schemas that don't parse
			continue
		}

		// Add x-kubernetes-group-version-kind extension for kubectl validation
		schemaObj["x-kubernetes-group-version-kind"] = []map[string]interface{}{
			{
				"group":   res.GVK.Group,
				"version": res.GVK.Version,
				"kind":    res.GVK.Kind,
			},
		}

		// Add schema to components
		schemaName := gv.String() + "." + res.GVK.Kind
		schemas[schemaName] = schemaObj

		// Add path entries for this resource
		basePath := "/apis/" + group + "/" + version + "/namespaces/{namespace}/" + res.Plural
		paths := spec["paths"].(map[string]interface{})

		// Schema reference for responses
		schemaRef := "#/components/schemas/" + schemaName

		// Common parameters
		namespaceParam := map[string]interface{}{
			"name":        "namespace",
			"in":          "path",
			"required":    true,
			"schema":      map[string]interface{}{"type": "string"},
			"description": "object namespace",
		}
		nameParam := map[string]interface{}{
			"name":        "name",
			"in":          "path",
			"required":    true,
			"schema":      map[string]interface{}{"type": "string"},
			"description": "name of the " + res.GVK.Kind,
		}

		// Collection operations
		paths[basePath] = map[string]interface{}{
			"parameters": []interface{}{namespaceParam},
			"get": map[string]interface{}{
				"description": "list " + res.Plural,
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "OK",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": schemaRef,
								},
							},
						},
					},
				},
			},
			"post": map[string]interface{}{
				"description": "create a " + res.GVK.Kind,
				"responses": map[string]interface{}{
					"201": map[string]interface{}{
						"description": "Created",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": schemaRef,
								},
							},
						},
					},
				},
			},
		}

		// Individual resource operations
		itemPath := basePath + "/{name}"
		paths[itemPath] = map[string]interface{}{
			"parameters": []interface{}{namespaceParam, nameParam},
			"get": map[string]interface{}{
				"description": "read the specified " + res.GVK.Kind,
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "OK",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": schemaRef,
								},
							},
						},
					},
				},
			},
			"put": map[string]interface{}{
				"description": "replace the specified " + res.GVK.Kind,
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "OK",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": schemaRef,
								},
							},
						},
					},
				},
			},
			"delete": map[string]interface{}{
				"description": "delete a " + res.GVK.Kind,
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "OK",
					},
				},
			},
		}

		// Status subresource
		statusPath := itemPath + "/status"
		paths[statusPath] = map[string]interface{}{
			"parameters": []interface{}{namespaceParam, nameParam},
			"get": map[string]interface{}{
				"description": "read status of the specified " + res.GVK.Kind,
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "OK",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": schemaRef,
								},
							},
						},
					},
				},
			},
			"put": map[string]interface{}{
				"description": "replace status of the specified " + res.GVK.Kind,
				"responses": map[string]interface{}{
					"200": map[string]interface{}{
						"description": "OK",
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"$ref": schemaRef,
								},
							},
						},
					},
				},
			},
		}
	}

	if len(schemas) == 0 {
		writeError(w, http.StatusNotFound, "group version not found")
		return
	}

	spec["components"] = map[string]interface{}{
		"schemas": schemas,
	}

	// Debug: print the schema structure to see what we're returning
	if len(schemas) > 0 {
		fmt.Printf("[DEBUG] Returning OpenAPI v3 schema with %d schemas and %d paths\n", len(schemas), len(spec["paths"].(map[string]interface{})))
		for schemaName := range schemas {
			fmt.Printf("[DEBUG]   Schema: %s\n", schemaName)
		}
		// Write the spec to a temp file for inspection
		if debugFile, err := os.Create("/tmp/openapi-v3-debug.json"); err == nil {
			defer debugFile.Close()
			encoder := json.NewEncoder(debugFile)
			encoder.SetIndent("", "  ")
			encoder.Encode(spec)
			fmt.Printf("[DEBUG] Wrote OpenAPI v3 spec to /tmp/openapi-v3-debug.json\n")
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(spec)
}

// OpenAPIV2 handles GET /openapi/v2
// Returns the OpenAPI v2 (Swagger 2.0) specification for all APIs.
func (h *DiscoveryHandler) OpenAPIV2(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("[DEBUG] OpenAPIV2 request: %s %s (Accept: %s)\n", r.Method, r.URL.Path, r.Header.Get("Accept"))

	// Check Accept header for protobuf request
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "protobuf") || strings.Contains(accept, "proto-openapi") {
		// kubectl requested protobuf but we don't support it yet
		// Return JSON anyway with correct Content-Type - kubectl should handle gracefully
		fmt.Printf("[DEBUG] Protobuf requested but not supported, returning JSON\n")
	}

	// Build OpenAPI v2 (Swagger 2.0) document
	spec := map[string]interface{}{
		"swagger": "2.0",
		"info": map[string]interface{}{
			"title":   "Orlop API",
			"version": "v1",
		},
		"paths":       map[string]interface{}{},
		"definitions": map[string]interface{}{},
	}

	definitions := spec["definitions"].(map[string]interface{})
	paths := spec["paths"].(map[string]interface{})

	// Group resources by group/version
	groupedResources := make(map[string][]ResourceInfo)
	for _, res := range h.resources {
		key := res.GVK.Group + "/" + res.GVK.Version
		groupedResources[key] = append(groupedResources[key], res)
	}

	// Add definitions and paths for each resource
	for gv, resources := range groupedResources {
		for _, res := range resources {
			// Parse the schema YAML
			var schemaObj map[string]interface{}
			if err := yaml.Unmarshal([]byte(res.SchemaYAML), &schemaObj); err != nil {
				continue
			}

			// Add x-kubernetes-group-version-kind extension
			schemaObj["x-kubernetes-group-version-kind"] = []map[string]interface{}{
				{
					"group":   res.GVK.Group,
					"version": res.GVK.Version,
					"kind":    res.GVK.Kind,
				},
			}

			// Add definition
			defName := res.GVK.Group + "." + res.GVK.Version + "." + res.GVK.Kind
			definitions[defName] = schemaObj

			// Add paths for this resource
			basePath := fmt.Sprintf("/apis/%s/namespaces/{namespace}/%s", gv, res.Plural)

			// Collection operations
			paths[basePath] = map[string]interface{}{
				"get": map[string]interface{}{
					"description": fmt.Sprintf("list objects of kind %s", res.GVK.Kind),
					"operationId": fmt.Sprintf("list%s%s", res.GVK.Version, res.GVK.Kind),
					"produces":    []string{"application/json"},
					"parameters": []interface{}{
						map[string]interface{}{
							"name":        "namespace",
							"in":          "path",
							"required":    true,
							"type":        "string",
							"description": "object name and auth scope, such as for teams and projects",
						},
						map[string]interface{}{
							"name":        "labelSelector",
							"in":          "query",
							"type":        "string",
							"description": "A selector to restrict the list of returned objects by their labels",
						},
						map[string]interface{}{
							"name":        "watch",
							"in":          "query",
							"type":        "boolean",
							"description": "Watch for changes to the described resources",
						},
						map[string]interface{}{
							"name":        "resourceVersion",
							"in":          "query",
							"type":        "string",
							"description": "When specified with watch, shows changes that occur after that version",
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "OK",
							"schema": map[string]interface{}{
								"$ref": fmt.Sprintf("#/definitions/%s", defName),
							},
						},
					},
				},
				"post": map[string]interface{}{
					"description": fmt.Sprintf("create a %s", res.GVK.Kind),
					"operationId": fmt.Sprintf("create%s%s", res.GVK.Version, res.GVK.Kind),
					"produces":    []string{"application/json"},
					"consumes":    []string{"application/json"},
					"parameters": []interface{}{
						map[string]interface{}{
							"name":     "namespace",
							"in":       "path",
							"required": true,
							"type":     "string",
						},
						map[string]interface{}{
							"name":     "body",
							"in":       "body",
							"required": true,
							"schema": map[string]interface{}{
								"$ref": fmt.Sprintf("#/definitions/%s", defName),
							},
						},
					},
					"responses": map[string]interface{}{
						"201": map[string]interface{}{
							"description": "Created",
							"schema": map[string]interface{}{
								"$ref": fmt.Sprintf("#/definitions/%s", defName),
							},
						},
					},
				},
			}

			// Individual resource operations
			itemPath := basePath + "/{name}"
			paths[itemPath] = map[string]interface{}{
				"get": map[string]interface{}{
					"description": fmt.Sprintf("read the specified %s", res.GVK.Kind),
					"operationId": fmt.Sprintf("read%s%s", res.GVK.Version, res.GVK.Kind),
					"produces":    []string{"application/json"},
					"parameters": []interface{}{
						map[string]interface{}{
							"name":     "namespace",
							"in":       "path",
							"required": true,
							"type":     "string",
						},
						map[string]interface{}{
							"name":        "name",
							"in":          "path",
							"required":    true,
							"type":        "string",
							"description": "name of the resource",
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "OK",
							"schema": map[string]interface{}{
								"$ref": fmt.Sprintf("#/definitions/%s", defName),
							},
						},
					},
				},
				"put": map[string]interface{}{
					"description": fmt.Sprintf("replace the specified %s", res.GVK.Kind),
					"operationId": fmt.Sprintf("replace%s%s", res.GVK.Version, res.GVK.Kind),
					"produces":    []string{"application/json"},
					"consumes":    []string{"application/json"},
					"parameters": []interface{}{
						map[string]interface{}{
							"name":     "namespace",
							"in":       "path",
							"required": true,
							"type":     "string",
						},
						map[string]interface{}{
							"name":     "name",
							"in":       "path",
							"required": true,
							"type":     "string",
						},
						map[string]interface{}{
							"name":     "body",
							"in":       "body",
							"required": true,
							"schema": map[string]interface{}{
								"$ref": fmt.Sprintf("#/definitions/%s", defName),
							},
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "OK",
							"schema": map[string]interface{}{
								"$ref": fmt.Sprintf("#/definitions/%s", defName),
							},
						},
					},
				},
				"delete": map[string]interface{}{
					"description": fmt.Sprintf("delete a %s", res.GVK.Kind),
					"operationId": fmt.Sprintf("delete%s%s", res.GVK.Version, res.GVK.Kind),
					"produces":    []string{"application/json"},
					"parameters": []interface{}{
						map[string]interface{}{
							"name":     "namespace",
							"in":       "path",
							"required": true,
							"type":     "string",
						},
						map[string]interface{}{
							"name":     "name",
							"in":       "path",
							"required": true,
							"type":     "string",
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "OK",
						},
					},
				},
			}

			// Status subresource
			statusPath := itemPath + "/status"
			paths[statusPath] = map[string]interface{}{
				"get": map[string]interface{}{
					"description": fmt.Sprintf("read status of the specified %s", res.GVK.Kind),
					"operationId": fmt.Sprintf("read%s%sStatus", res.GVK.Version, res.GVK.Kind),
					"produces":    []string{"application/json"},
					"parameters": []interface{}{
						map[string]interface{}{
							"name":     "namespace",
							"in":       "path",
							"required": true,
							"type":     "string",
						},
						map[string]interface{}{
							"name":     "name",
							"in":       "path",
							"required": true,
							"type":     "string",
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "OK",
							"schema": map[string]interface{}{
								"$ref": fmt.Sprintf("#/definitions/%s", defName),
							},
						},
					},
				},
				"put": map[string]interface{}{
					"description": fmt.Sprintf("replace status of the specified %s", res.GVK.Kind),
					"operationId": fmt.Sprintf("replace%s%sStatus", res.GVK.Version, res.GVK.Kind),
					"produces":    []string{"application/json"},
					"consumes":    []string{"application/json"},
					"parameters": []interface{}{
						map[string]interface{}{
							"name":     "namespace",
							"in":       "path",
							"required": true,
							"type":     "string",
						},
						map[string]interface{}{
							"name":     "name",
							"in":       "path",
							"required": true,
							"type":     "string",
						},
						map[string]interface{}{
							"name":     "body",
							"in":       "body",
							"required": true,
							"schema": map[string]interface{}{
								"$ref": fmt.Sprintf("#/definitions/%s", defName),
							},
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "OK",
							"schema": map[string]interface{}{
								"$ref": fmt.Sprintf("#/definitions/%s", defName),
							},
						},
					},
				},
			}
		}
	}

	// Determine response format based on Accept header
	if strings.Contains(accept, "protobuf") || strings.Contains(accept, "proto-openapi") {
		// Return 501 Not Implemented for protobuf requests
		// kubectl will fall back to using OpenAPI v3
		http.Error(w, "Protobuf encoding for OpenAPI v2 not implemented. Use OpenAPI v3 instead.", http.StatusNotImplemented)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(spec)
}
