package crds

import (
	"context"
	"strings"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

// GetSupportedVersions returns the list of supported versions for a CRD
// using the Kubernetes Discovery API (does not require RBAC for CRDs).
// The name parameter should be in the format "resource.group" (e.g., "clusters.cluster.x-k8s.io").
func GetSupportedVersions(ctx context.Context, config *rest.Config, name string) ([]string, error) {
	// Parse the CRD name to extract group (format: "resource.group")
	parts := strings.SplitN(name, ".", 2)
	if len(parts) != 2 {
		return nil, nil
	}
	group := parts[1]

	// Create discovery client using the provided REST config
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}

	// Get all API groups
	apiGroupList, err := discoveryClient.ServerGroups()
	if err != nil {
		return nil, err
	}

	// Find versions for our group
	versions := make([]string, 0)
	for _, apiGroup := range apiGroupList.Groups {
		if apiGroup.Name == group {
			for _, version := range apiGroup.Versions {
				versions = append(versions, version.Version)
			}
			break
		}
	}

	return versions, nil
}
