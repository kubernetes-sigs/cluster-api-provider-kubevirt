package contract

import (
	"testing"

	"sigs.k8s.io/kustomize/api/filesys"
	"sigs.k8s.io/kustomize/api/krusty"
)

func TestCRDsHaveV1Beta2ContractLabel(t *testing.T) {
	kustomizer := krusty.MakeKustomizer(krusty.MakeDefaultOptions())
	resources, err := kustomizer.Run(filesys.MakeFsOnDisk(), "../../config/crd")
	if err != nil {
		t.Fatalf("failed to render CRD kustomization: %v", err)
	}

	manifests := resources.Resources()
	if len(manifests) == 0 {
		t.Fatal("expected rendered CRDs, got none")
	}

	for _, resource := range manifests {
		if resource.GetKind() != "CustomResourceDefinition" {
			continue
		}

		labels := resource.GetLabels("cluster.x-k8s.io/v1beta2")
		if got := labels["cluster.x-k8s.io/v1beta2"]; got != "v1alpha1" {
			t.Errorf("%s: cluster.x-k8s.io/v1beta2 label = %q, want %q", resource.CurId(), got, "v1alpha1")
		}
	}
}
