package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v2"

	"sigs.k8s.io/kustomize/api/filesys"
	"sigs.k8s.io/kustomize/api/krusty"

	kustomizetypes "sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/resid"

	cloudprovider "kubevirt.io/cloud-provider-kubevirt/pkg/provider"
)

func kustomize(kustomizationPath, base string, clusterName string, namespace string) error {

	kustomizationFilePath := filepath.Join(kustomizationPath, "kustomization.yaml")
	kustomizationFile, err := os.Create(kustomizationFilePath)
	if err != nil {
		return fmt.Errorf("failed opening cloud provider kustomization file: %v", err)
	}

	kustomization := kustomizetypes.Kustomization{
		Namespace: namespace,
		Bases:     []string{base},
		PatchesJson6902: []kustomizetypes.Patch{
			{
				Patch: fmt.Sprintf(`
- op: add
  path: /metadata/namespace
  value: kube-system
- op: add
  path: /subjects/0/namespace
  value: %s
`, namespace),
				Target: &kustomizetypes.Selector{
					ResId: resid.ResId{
						Gvk: resid.Gvk{
							Group:   "rbac.authorization.k8s.io",
							Version: "v1",
							Kind:    "RoleBinding",
						},
						Name: "kccm",
					},
				},
			},
			{
				Patch: fmt.Sprintf(`
- op: add
  path: /spec/template/spec/volumes/-
  value:
    secret:
      secretName: %s-kubeconfig
    name: kubeconfig
`, clusterName),
				Target: &kustomizetypes.Selector{
					ResId: resid.ResId{
						Gvk: resid.Gvk{
							Group:   "apps",
							Version: "v1",
							Kind:    "Deployment",
						},
						Name: "kubevirt-cloud-controller-manager",
					},
				},
			},
		},
		GeneratorOptions: &kustomizetypes.GeneratorOptions{
			DisableNameSuffixHash: true,
		},
		ConfigMapGenerator: []kustomizetypes.ConfigMapArgs{
			{
				GeneratorArgs: kustomizetypes.GeneratorArgs{
					Namespace: namespace,
					Name:      "cloud-config",
					KvPairSources: kustomizetypes.KvPairSources{
						FileSources: []string{"cloud-config"},
					},
				},
			},
		},
	}

	if err := yaml.NewEncoder(kustomizationFile).Encode(&kustomization); err != nil {
		return fmt.Errorf("failed decoding kustomization yaml file: %v", err)
	}

	cloudConfig := cloudprovider.CloudConfig{
		LoadBalancer: cloudprovider.LoadBalancerConfig{
			Enabled:              true,
			CreationPollInterval: 30,
		},
		Namespace: namespace,
	}

	cloudConfigFile, err := os.Create(filepath.Join(kustomizationPath, "cloud-config"))
	if err != nil {
		return fmt.Errorf("failed creating kccm cloud config file: %v", err)
	}

	if err := yaml.NewEncoder(cloudConfigFile).Encode(&cloudConfig); err != nil {
		return fmt.Errorf("failed encoding kccm cloud config to yaml: %v", err)
	}

	return nil
}

func main() {
	var base = flag.String("base", "https://github.com/qinqon/cloud-provider-kubevirt/config/base?ref=kustomize-overlays-cm-secrets", "kccm kustomize base URL")
	var templatePath = flag.String("template", "../../templates/cluster-template.yaml", "Cluster template to generate flavor for it will append 'kccm' to the file base name")
	flag.Parse()

	kustomizationPath, err := ioutil.TempDir("/tmp", "kustomize-overlay-capk-kccm")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(kustomizationPath)

	if err := kustomize(kustomizationPath, *base, "${CLUSTER_NAME}", "${NAMESPACE}"); err != nil {
		panic(err)
	}

	fSys := filesys.MakeFsOnDisk()

	k := krusty.MakeKustomizer(krusty.MakeDefaultOptions())
	m, err := k.Run(fSys, kustomizationPath)
	if err != nil {
		panic(fmt.Errorf("failed running cloud provider kustomize: %v", err))
	}
	yamlTemplate, err := m.AsYaml()
	if err != nil {
		panic(fmt.Errorf("failed marshaling template as YAML: %v", err))
	}
	flavorPath := fmt.Sprintf("%s-kccm.yaml", strings.TrimSuffix(*templatePath, filepath.Ext(*templatePath)))
	flavorPathFile, err := os.Create(flavorPath)
	if err != nil {
		panic(fmt.Errorf("failed creating kccm flavor file: %v", err))
	}
	clusterTemplate, err := ioutil.ReadFile(*templatePath)
	if err != nil {
		panic(fmt.Errorf("failed reading cluster template file: %v", err))
	}

	flavorPathFile.Write(clusterTemplate)
	flavorPathFile.Write([]byte("---\n"))
	flavorPathFile.Write(yamlTemplate)

	// Add authorization to extension-apiserver-authorization-reading using
	// a ClusterRoleBinding since clusterctl it's going to replace namespace
	// "kube-system" at RoleBinding [1]
	// [1] https://github.com/kubernetes-sigs/cluster-api/blob/6b042456383aa18ba61fc5de5c41901dd7267c60/cmd/clusterctl/client/repository/template.go#L131
	flavorPathFile.Write([]byte(`---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: extension-apiserver-authentication-reader
rules:
- apiGroups:
  - ""
  resourceNames:
  - extension-apiserver-authentication
  resources:
  - configmaps
  verbs:
  - get
  - list
  - watch
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: kccm-extension-apiserver-authentication-reader
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: extension-apiserver-authentication-reader
subjects:
- kind: ServiceAccount
  name: cloud-controller-manager
  namespace: ${NAMESPACE}
`))
	fmt.Printf("capk kccm flavor generated at %s\n", flavorPath)
}
