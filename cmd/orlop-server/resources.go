package main

import (
	privatev1 "github.com/thetechnick/orlop/apis/private/test/v1"
	publicv1 "github.com/thetechnick/orlop/apis/public/test/v1"
	"github.com/thetechnick/orlop/pkg/apiserver"
	"k8s.io/apimachinery/pkg/runtime"
)

// getPrivateResources returns the resource definitions for the private API.
// Uses generated ResourceInfo from the private API package.
func getPrivateResources() []apiserver.ResourceInfo {
	// Convert from privatev1.ResourceInfo to apiserver.ResourceInfo
	privateInfos := privatev1.GetResourceInfos()
	result := make([]apiserver.ResourceInfo, len(privateInfos))
	for i, info := range privateInfos {
		result[i] = apiserver.ResourceInfo{
			GVK:            info.GVK,
			Plural:         info.Plural,
			SchemaYAML:     info.SchemaYAML,
			PrivateNewFunc: nil, // Not used for private API
		}
	}
	return result
}

// getPublicResources returns the resource definitions for the public API.
// Uses generated ResourceInfo from both public and private API packages.
func getPublicResources() []apiserver.ResourceInfo {
	// Get public API resource infos - just use them directly
	// PrivateNewFunc is no longer needed since we use schemes
	return publicv1.GetResourceInfos()
}

// getPrivateScheme creates and returns a runtime.Scheme with private API types registered.
func getPrivateScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	privatev1.AddToScheme(scheme)
	return scheme
}

// getPublicScheme creates and returns a runtime.Scheme with public API types registered.
func getPublicScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	publicv1.AddToScheme(scheme)
	return scheme
}
