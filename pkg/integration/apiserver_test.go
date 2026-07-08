package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/thetechnick/orlop/pkg/apiserver"
)

var (
	baseURL string
	server  *apiserver.Server
)

func TestMain(m *testing.M) {
	// Start server on random port
	opts := apiserver.Options{
		Address:        "127.0.0.1",
		PrivatePort:    8765, // Use fixed port for testing
		PublicPort:     8766,
		CORSOrigins:    []string{"*"},
		EnablePublicAPI: false, // Disable public API for existing tests
	}

	var err error
	server, err = apiserver.New(opts)
	if err != nil {
		panic(fmt.Sprintf("Failed to create server: %v", err))
	}

	baseURL = fmt.Sprintf("http://%s", server.PrivateAddress())

	// Start server in background
	go func() {
		if err := server.Run(); err != nil && err != http.ErrServerClosed {
			panic(fmt.Sprintf("Server error: %v", err))
		}
	}()

	// Wait for server to be ready
	time.Sleep(100 * time.Millisecond)

	// Run tests
	code := m.Run()

	// Shutdown server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(ctx)

	// Exit with test result code
	if code != 0 {
		panic("Tests failed")
	}
}

func TestObjectCRUD(t *testing.T) {
	namespace := "default"
	name := "test-object"

	// Create object
	createPayload := map[string]interface{}{
		"apiVersion": "test.orlop.thetechnick.ninja/v1",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"publicField":   "public-value",
			"internalField": "internal-value",
			"nested": map[string]interface{}{
				"publicField":   "nested-public",
				"internalField": "nested-internal",
			},
		},
	}

	resp, body := doRequest(t, "POST", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects", namespace), createPayload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Expected status 201, got %d: %s", resp.StatusCode, body)
	}

	var created map[string]interface{}
	json.Unmarshal([]byte(body), &created)

	// Check that object was created with metadata
	metadata := created["metadata"].(map[string]interface{})
	if metadata["name"] != name {
		t.Errorf("Expected name %s, got %v", name, metadata["name"])
	}
	if metadata["namespace"] != namespace {
		t.Errorf("Expected namespace %s, got %v", namespace, metadata["namespace"])
	}
	if metadata["resourceVersion"] == nil {
		t.Error("Expected resourceVersion to be set")
	}

	resourceVersion := metadata["resourceVersion"].(string)

	// Get object
	resp, body = doRequest(t, "GET", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects/%s", namespace, name), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", resp.StatusCode, body)
	}

	var retrieved map[string]interface{}
	json.Unmarshal([]byte(body), &retrieved)
	if retrieved["metadata"].(map[string]interface{})["name"] != name {
		t.Error("Retrieved object has wrong name")
	}

	// List objects
	resp, body = doRequest(t, "GET", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects", namespace), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", resp.StatusCode, body)
	}

	var list map[string]interface{}
	json.Unmarshal([]byte(body), &list)
	items := list["items"].([]interface{})
	if len(items) == 0 {
		t.Error("Expected at least one object in list")
	}

	// Update object
	updatePayload := map[string]interface{}{
		"apiVersion": "test.orlop.thetechnick.ninja/v1",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":            name,
			"namespace":       namespace,
			"resourceVersion": resourceVersion,
		},
		"spec": map[string]interface{}{
			"publicField":   "updated-value",
			"internalField": "internal-value",
			"nested": map[string]interface{}{
				"publicField":   "nested-public",
				"internalField": "nested-internal",
			},
		},
	}

	resp, body = doRequest(t, "PUT", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects/%s", namespace, name), updatePayload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", resp.StatusCode, body)
	}

	var updated map[string]interface{}
	json.Unmarshal([]byte(body), &updated)
	spec := updated["spec"].(map[string]interface{})
	if spec["publicField"] != "updated-value" {
		t.Errorf("Expected publicField to be updated, got %v", spec["publicField"])
	}

	// Delete object
	resp, body = doRequest(t, "DELETE", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects/%s", namespace, name), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", resp.StatusCode, body)
	}

	// Verify object is deleted
	resp, _ = doRequest(t, "GET", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects/%s", namespace, name), nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Expected status 404 after delete, got %d", resp.StatusCode)
	}
}

func TestDefaulting(t *testing.T) {
	namespace := "default"
	name := "test-defaulting"

	// Create object without defaultField
	createPayload := map[string]interface{}{
		"apiVersion": "test.orlop.thetechnick.ninja/v1",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"publicField":   "public-value",
			"internalField": "internal-value",
			"nested": map[string]interface{}{
				"publicField":   "nested-public",
				"internalField": "nested-internal",
			},
		},
	}

	resp, body := doRequest(t, "POST", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects", namespace), createPayload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Expected status 201, got %d: %s", resp.StatusCode, body)
	}

	var created map[string]interface{}
	json.Unmarshal([]byte(body), &created)

	// Check that defaultField was set to "default-value"
	spec := created["spec"].(map[string]interface{})
	if spec["defaultField"] != "default-value" {
		t.Errorf("Expected defaultField to be 'default-value', got %v", spec["defaultField"])
	}

	// Cleanup
	doRequest(t, "DELETE", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects/%s", namespace, name), nil)
}

func TestPruning(t *testing.T) {
	namespace := "default"
	name := "test-pruning"

	// Create object with unknown field
	createPayload := map[string]interface{}{
		"apiVersion": "test.orlop.thetechnick.ninja/v1",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"publicField":   "public-value",
			"internalField": "internal-value",
			"unknownField":  "should-be-pruned",
			"nested": map[string]interface{}{
				"publicField":   "nested-public",
				"internalField": "nested-internal",
			},
		},
	}

	resp, body := doRequest(t, "POST", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects", namespace), createPayload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Expected status 201, got %d: %s", resp.StatusCode, body)
	}

	var created map[string]interface{}
	json.Unmarshal([]byte(body), &created)

	// Check that unknownField was pruned
	spec := created["spec"].(map[string]interface{})
	if _, exists := spec["unknownField"]; exists {
		t.Error("Expected unknownField to be pruned, but it still exists")
	}

	// Cleanup
	doRequest(t, "DELETE", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects/%s", namespace, name), nil)
}

func TestValidation(t *testing.T) {
	namespace := "default"
	name := "test-validation"

	// Create object missing required field
	createPayload := map[string]interface{}{
		"apiVersion": "test.orlop.thetechnick.ninja/v1",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"publicField": "public-value",
			// Missing internalField and nested (required fields)
		},
	}

	resp, body := doRequest(t, "POST", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects", namespace), createPayload)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("Expected status 400 for validation error, got %d: %s", resp.StatusCode, body)
	}

	// Verify error message mentions validation
	if !contains(body, "validation") && !contains(body, "required") {
		t.Errorf("Expected validation error message, got: %s", body)
	}
}

func TestStatusSubresource(t *testing.T) {
	namespace := "default"
	name := "test-status"

	// Create object
	createPayload := map[string]interface{}{
		"apiVersion": "test.orlop.thetechnick.ninja/v1",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"publicField":   "public-value",
			"internalField": "internal-value",
			"nested": map[string]interface{}{
				"publicField":   "nested-public",
				"internalField": "nested-internal",
			},
		},
	}

	resp, body := doRequest(t, "POST", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects", namespace), createPayload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Expected status 201, got %d: %s", resp.StatusCode, body)
	}

	var created map[string]interface{}
	json.Unmarshal([]byte(body), &created)
	resourceVersion := created["metadata"].(map[string]interface{})["resourceVersion"].(string)
	createdGeneration := int64(created["metadata"].(map[string]interface{})["generation"].(float64))

	if createdGeneration != 1 {
		t.Errorf("Expected generation 1 on create, got %d", createdGeneration)
	}

	// Update status
	statusPayload := map[string]interface{}{
		"metadata": map[string]interface{}{
			"resourceVersion": resourceVersion,
		},
		"status": map[string]interface{}{
			"conditions": []string{"Ready", "Healthy"},
		},
	}

	resp, body = doRequest(t, "PUT", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects/%s/status", namespace, name), statusPayload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", resp.StatusCode, body)
	}

	var updated map[string]interface{}
	json.Unmarshal([]byte(body), &updated)

	// Check that status was updated
	status := updated["status"].(map[string]interface{})
	conditions := status["conditions"].([]interface{})
	if len(conditions) != 2 {
		t.Errorf("Expected 2 conditions, got %d", len(conditions))
	}

	// Check that spec was NOT modified
	spec := updated["spec"].(map[string]interface{})
	if spec["publicField"] != "public-value" {
		t.Error("Spec should not be modified by status update")
	}

	// Check that generation was NOT incremented for status update
	statusGeneration := int64(updated["metadata"].(map[string]interface{})["generation"].(float64))
	if statusGeneration != 1 {
		t.Errorf("Expected generation to remain 1 after status update, got %d", statusGeneration)
	}

	// Cleanup
	doRequest(t, "DELETE", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects/%s", namespace, name), nil)
}

func TestCORS(t *testing.T) {
	// Make an OPTIONS request (preflight)
	req, err := http.NewRequest("OPTIONS", baseURL+"/apis/test.orlop.thetechnick.ninja/v1/namespaces/default/objects", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Check CORS headers
	if resp.Header.Get("Access-Control-Allow-Origin") == "" {
		t.Error("Expected Access-Control-Allow-Origin header")
	}
	if resp.Header.Get("Access-Control-Allow-Methods") == "" {
		t.Error("Expected Access-Control-Allow-Methods header")
	}
}

func TestGenerationTracking(t *testing.T) {
	namespace := "default"
	name := "test-generation"

	// Create object
	createPayload := map[string]interface{}{
		"apiVersion": "test.orlop.thetechnick.ninja/v1",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"publicField":   "initial-value",
			"internalField": "internal-value",
			"nested": map[string]interface{}{
				"publicField":   "nested-public",
				"internalField": "nested-internal",
			},
		},
	}

	resp, body := doRequest(t, "POST", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects", namespace), createPayload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Expected status 201, got %d: %s", resp.StatusCode, body)
	}

	var created map[string]interface{}
	json.Unmarshal([]byte(body), &created)

	generation := int64(created["metadata"].(map[string]interface{})["generation"].(float64))
	if generation != 1 {
		t.Errorf("Expected generation 1 on create, got %d", generation)
	}

	// Update spec - generation should increment
	resourceVersion := created["metadata"].(map[string]interface{})["resourceVersion"].(string)
	updatePayload := map[string]interface{}{
		"apiVersion": "test.orlop.thetechnick.ninja/v1",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":            name,
			"namespace":       namespace,
			"resourceVersion": resourceVersion,
		},
		"spec": map[string]interface{}{
			"publicField":   "updated-value",
			"internalField": "internal-value",
			"nested": map[string]interface{}{
				"publicField":   "nested-public",
				"internalField": "nested-internal",
			},
		},
	}

	resp, body = doRequest(t, "PUT", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects/%s", namespace, name), updatePayload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", resp.StatusCode, body)
	}

	var updated map[string]interface{}
	json.Unmarshal([]byte(body), &updated)

	generation = int64(updated["metadata"].(map[string]interface{})["generation"].(float64))
	if generation != 2 {
		t.Errorf("Expected generation 2 after spec update, got %d", generation)
	}

	// Update with same spec - generation should NOT increment
	resourceVersion = updated["metadata"].(map[string]interface{})["resourceVersion"].(string)
	updatePayload["metadata"].(map[string]interface{})["resourceVersion"] = resourceVersion

	resp, body = doRequest(t, "PUT", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects/%s", namespace, name), updatePayload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", resp.StatusCode, body)
	}

	var updated2 map[string]interface{}
	json.Unmarshal([]byte(body), &updated2)

	generation = int64(updated2["metadata"].(map[string]interface{})["generation"].(float64))
	if generation != 2 {
		t.Errorf("Expected generation to remain 2 when spec unchanged, got %d", generation)
	}

	// Cleanup
	doRequest(t, "DELETE", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects/%s", namespace, name), nil)
}

func TestLabelSelector(t *testing.T) {
	namespace := "default"

	// Create objects with different labels
	objects := []struct {
		name   string
		labels map[string]string
	}{
		{"obj-1", map[string]string{"env": "prod", "tier": "frontend"}},
		{"obj-2", map[string]string{"env": "dev", "tier": "backend"}},
		{"obj-3", map[string]string{"env": "prod", "tier": "backend"}},
		{"obj-4", map[string]string{"env": "staging", "tier": "frontend"}},
	}

	// Create all objects
	for _, obj := range objects {
		createPayload := map[string]interface{}{
			"apiVersion": "test.orlop.thetechnick.ninja/v1",
			"kind":       "Object",
			"metadata": map[string]interface{}{
				"name":      obj.name,
				"namespace": namespace,
				"labels":    obj.labels,
			},
			"spec": map[string]interface{}{
				"publicField":   "test",
				"internalField": "internal",
				"nested": map[string]interface{}{
					"publicField":   "nested",
					"internalField": "nested-internal",
				},
			},
		}

		resp, body := doRequest(t, "POST", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects", namespace), createPayload)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("Expected status 201, got %d: %s", resp.StatusCode, body)
		}
	}

	// Test cases for label selectors
	testCases := []struct {
		name     string
		selector string
		expected []string
	}{
		{
			name:     "Equality selector - env=prod",
			selector: "env=prod",
			expected: []string{"obj-1", "obj-3"},
		},
		{
			name:     "Equality selector - tier=frontend",
			selector: "tier=frontend",
			expected: []string{"obj-1", "obj-4"},
		},
		{
			name:     "Multiple selectors - env=prod,tier=backend",
			selector: "env=prod,tier=backend",
			expected: []string{"obj-3"},
		},
		{
			name:     "Inequality selector - env!=prod",
			selector: "env!=prod",
			expected: []string{"obj-2", "obj-4"},
		},
		{
			name:     "Set-based selector - env in (prod,staging)",
			selector: "env in (prod,staging)",
			expected: []string{"obj-1", "obj-3", "obj-4"},
		},
		{
			name:     "Set-based selector - tier notin (frontend)",
			selector: "tier notin (frontend)",
			expected: []string{"obj-2", "obj-3"},
		},
		{
			name:     "Empty selector",
			selector: "",
			expected: []string{"obj-1", "obj-2", "obj-3", "obj-4"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			path := fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects", namespace)
			if tc.selector != "" {
				path += "?labelSelector=" + url.QueryEscape(tc.selector)
			}

			resp, body := doRequest(t, "GET", path, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("Expected status 200, got %d: %s", resp.StatusCode, body)
			}

			var list map[string]interface{}
			json.Unmarshal([]byte(body), &list)
			items := list["items"].([]interface{})

			// Extract names from results
			var resultNames []string
			for _, item := range items {
				itemMap := item.(map[string]interface{})
				metadata := itemMap["metadata"].(map[string]interface{})
				resultNames = append(resultNames, metadata["name"].(string))
			}

			// Sort both slices for comparison
			sort.Strings(resultNames)
			sort.Strings(tc.expected)

			if !reflect.DeepEqual(resultNames, tc.expected) {
				t.Errorf("Expected %v, got %v", tc.expected, resultNames)
			}
		})
	}

	// Test invalid label selector
	t.Run("Invalid label selector", func(t *testing.T) {
		path := fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects", namespace)
		path += "?labelSelector=" + url.QueryEscape("invalid!selector")
		resp, body := doRequest(t, "GET", path, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("Expected status 400 for invalid selector, got %d: %s", resp.StatusCode, body)
		}

		if !contains(body, "invalid label selector") {
			t.Errorf("Expected error message about invalid label selector, got: %s", body)
		}
	})

	// Cleanup
	for _, obj := range objects {
		doRequest(t, "DELETE", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects/%s", namespace, obj.name), nil)
	}
}

func TestDiscoveryAPIGroupList(t *testing.T) {
	resp, body := doRequest(t, "GET", "/apis", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", resp.StatusCode, body)
	}

	var groupList map[string]interface{}
	if err := json.Unmarshal([]byte(body), &groupList); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	// Check that we have the test.orlop.thetechnick.ninja group
	groups := groupList["groups"].([]interface{})
	found := false
	for _, g := range groups {
		group := g.(map[string]interface{})
		if group["name"] == "test.orlop.thetechnick.ninja" {
			found = true
			// Check that it has v1 version
			versions := group["versions"].([]interface{})
			if len(versions) == 0 {
				t.Error("Expected at least one version")
			}
			v := versions[0].(map[string]interface{})
			if v["version"] != "v1" {
				t.Errorf("Expected version v1, got %s", v["version"])
			}
			break
		}
	}

	if !found {
		t.Error("Expected test.orlop.thetechnick.ninja group not found")
	}
}

func TestDiscoveryAPIGroup(t *testing.T) {
	resp, body := doRequest(t, "GET", "/apis/test.orlop.thetechnick.ninja", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", resp.StatusCode, body)
	}

	var group map[string]interface{}
	if err := json.Unmarshal([]byte(body), &group); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	// Check group name
	if group["name"] != "test.orlop.thetechnick.ninja" {
		t.Errorf("Expected group name test.orlop.thetechnick.ninja, got %s", group["name"])
	}

	// Check versions
	versions := group["versions"].([]interface{})
	if len(versions) == 0 {
		t.Error("Expected at least one version")
	}
}

func TestDiscoveryAPIResourceList(t *testing.T) {
	resp, body := doRequest(t, "GET", "/apis/test.orlop.thetechnick.ninja/v1", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", resp.StatusCode, body)
	}

	var resourceList map[string]interface{}
	if err := json.Unmarshal([]byte(body), &resourceList); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	// Check groupVersion
	if resourceList["groupVersion"] != "test.orlop.thetechnick.ninja/v1" {
		t.Errorf("Expected groupVersion test.orlop.thetechnick.ninja/v1, got %s", resourceList["groupVersion"])
	}

	// Check resources (field is called "resources" in JSON, not "apiResources")
	if resourceList["resources"] == nil {
		t.Fatalf("resources is nil. Full response: %s", body)
	}
	resources := resourceList["resources"].([]interface{})
	if len(resources) == 0 {
		t.Error("Expected at least one resource")
	}

	// Find objects resource
	foundObjects := false
	foundObjectsStatus := false
	for _, r := range resources {
		res := r.(map[string]interface{})
		if res["name"] == "objects" {
			foundObjects = true
			if res["kind"] != "Object" {
				t.Errorf("Expected kind Object, got %s", res["kind"])
			}
			if res["namespaced"] != true {
				t.Error("Expected namespaced to be true")
			}
		}
		if res["name"] == "objects/status" {
			foundObjectsStatus = true
		}
	}

	if !foundObjects {
		t.Error("Expected objects resource not found")
	}
	if !foundObjectsStatus {
		t.Error("Expected objects/status subresource not found")
	}
}

func TestDiscoveryOpenAPIV3(t *testing.T) {
	resp, body := doRequest(t, "GET", "/openapi/v3", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", resp.StatusCode, body)
	}

	var openapi map[string]interface{}
	if err := json.Unmarshal([]byte(body), &openapi); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	// Check paths
	paths := openapi["paths"].(map[string]interface{})
	if len(paths) == 0 {
		t.Error("Expected at least one path")
	}

	// Check for test group/version
	key := "apis/test.orlop.thetechnick.ninja/v1"
	if _, ok := paths[key]; !ok {
		t.Errorf("Expected path %s not found", key)
	}
}

func TestDiscoveryOpenAPIV3GroupVersion(t *testing.T) {
	resp, body := doRequest(t, "GET", "/openapi/v3/apis/test.orlop.thetechnick.ninja/v1", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", resp.StatusCode, body)
	}

	var spec map[string]interface{}
	if err := json.Unmarshal([]byte(body), &spec); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	// Check OpenAPI version
	if spec["openapi"] != "3.0.0" {
		t.Errorf("Expected openapi 3.0.0, got %s", spec["openapi"])
	}

	// Check info
	info := spec["info"].(map[string]interface{})
	if info["version"] != "v1" {
		t.Errorf("Expected version v1, got %s", info["version"])
	}

	// Check components and schemas
	components := spec["components"].(map[string]interface{})
	schemas := components["schemas"].(map[string]interface{})
	if len(schemas) == 0 {
		t.Error("Expected at least one schema")
	}

	// Check paths
	paths := spec["paths"].(map[string]interface{})
	if len(paths) == 0 {
		t.Error("Expected at least one path")
	}
}

func TestSharedResourceVersion(t *testing.T) {
	namespace := "default"

	// List objects initially - should get initial resource version
	resp, body := doRequest(t, "GET", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects", namespace), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", resp.StatusCode, body)
	}

	var initialList map[string]interface{}
	json.Unmarshal([]byte(body), &initialList)
	initialMetadata := initialList["metadata"].(map[string]interface{})
	initialRV := initialMetadata["resourceVersion"].(string)

	// Create an Object
	obj1Payload := map[string]interface{}{
		"apiVersion": "test.orlop.thetechnick.ninja/v1",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":      "test-obj",
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"publicField":   "test",
			"internalField": "internal",
			"nested": map[string]interface{}{
				"publicField":   "nested",
				"internalField": "nested-internal",
			},
		},
	}

	resp, body = doRequest(t, "POST", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects", namespace), obj1Payload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Expected status 201, got %d: %s", resp.StatusCode, body)
	}

	var createdObj map[string]interface{}
	json.Unmarshal([]byte(body), &createdObj)
	objMetadata := createdObj["metadata"].(map[string]interface{})
	objRV := objMetadata["resourceVersion"].(string)

	// ResourceVersion should have incremented
	if objRV == initialRV {
		t.Errorf("Expected resource version to increment after create, got %s", objRV)
	}

	// Create an Other (different resource type)
	other1Payload := map[string]interface{}{
		"apiVersion": "test.orlop.thetechnick.ninja/v1",
		"kind":       "Other",
		"metadata": map[string]interface{}{
			"name":      "test-other",
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"publicField":   "test",
			"internalField": "internal",
		},
	}

	resp, body = doRequest(t, "POST", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/others", namespace), other1Payload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Expected status 201, got %d: %s", resp.StatusCode, body)
	}

	var createdOther map[string]interface{}
	json.Unmarshal([]byte(body), &createdOther)
	otherMetadata := createdOther["metadata"].(map[string]interface{})
	otherRV := otherMetadata["resourceVersion"].(string)

	// ResourceVersion should continue incrementing across resource types
	if otherRV <= objRV {
		t.Errorf("Expected resource version to be shared across types and increment. Object RV: %s, Other RV: %s", objRV, otherRV)
	}

	// List objects - should return current resource version
	resp, body = doRequest(t, "GET", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects", namespace), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", resp.StatusCode, body)
	}

	var objectsList map[string]interface{}
	json.Unmarshal([]byte(body), &objectsList)
	listMetadata := objectsList["metadata"].(map[string]interface{})
	listRV := listMetadata["resourceVersion"].(string)

	// List resourceVersion should match the latest resourceVersion (from Other)
	if listRV != otherRV {
		t.Errorf("Expected list resourceVersion to be current (%s), got %s", otherRV, listRV)
	}

	// Cleanup
	doRequest(t, "DELETE", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/objects/test-obj", namespace), nil)
	doRequest(t, "DELETE", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/others/test-other", namespace), nil)
}

func TestOtherResource(t *testing.T) {
	namespace := "default"
	name := "test-other"

	// Create Other resource
	createPayload := map[string]interface{}{
		"apiVersion": "test.orlop.thetechnick.ninja/v1",
		"kind":       "Other",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"publicField":   "public-value",
			"internalField": "internal-value",
		},
	}

	resp, body := doRequest(t, "POST", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/others", namespace), createPayload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Expected status 201, got %d: %s", resp.StatusCode, body)
	}

	// Get Other resource
	resp, body = doRequest(t, "GET", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/others/%s", namespace, name), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d: %s", resp.StatusCode, body)
	}

	// Cleanup
	doRequest(t, "DELETE", fmt.Sprintf("/apis/test.orlop.thetechnick.ninja/v1/namespaces/%s/others/%s", namespace, name), nil)
}

// Helper functions

func doRequest(t *testing.T, method, path string, body interface{}) (*http.Response, string) {
	var reqBody io.Reader
	if body != nil {
		jsonData, _ := json.Marshal(body)
		reqBody = bytes.NewBuffer(jsonData)
	}

	req, err := http.NewRequest(method, baseURL+path, reqBody)
	if err != nil {
		t.Fatal(err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	return resp, string(respBody)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && contains(s[1:], substr) || s[:len(substr)] == substr)
}
