package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
)

func dump(ctx context.Context, kubeconfig, artifactsSuffix string) {
	cmd := exec.CommandContext(ctx, DumpPath, "--kubeconfig", kubeconfig)

	failureLocation := CurrentSpecReport().Failure.Location
	artifactsPath := filepath.Join(os.Getenv("ARTIFACTS"), fmt.Sprintf("%s:%d", filepath.Base(failureLocation.FileName), failureLocation.LineNumber), artifactsSuffix)
	cmd.Env = append(cmd.Env, fmt.Sprintf("ARTIFACTS=%s", artifactsPath))

	By(fmt.Sprintf("dumping k8s artifacts to %s", artifactsPath))
	output, err := cmd.CombinedOutput()
	Expect(err).To(Succeed(), string(output))

	By(fmt.Sprintf("dumping CAPK artifacts to %s", artifactsPath))
	dumpCAPKResources(ctx, artifactsPath)
}

func dumpCAPKResources(ctx context.Context, artifactsDir string) {
	GinkgoHelper()

	By("dump KubevirtClusters")
	kvClusterList := &infrav1.KubevirtClusterList{}
	Expect(k8sclient.List(ctx, kvClusterList, &client.ListOptions{})).To(Succeed())
	for i := range kvClusterList.Items {
		item := &kvClusterList.Items[i]
		item.SetManagedFields(nil)
	}

	Expect(
		dumpJsonFile(kvClusterList, filepath.Join(artifactsDir, "0_kubevirtclusters.json")),
	).To(Succeed())

	By("dump KubevirtClusterTemplates")
	kvClusterTmpltList := &infrav1.KubevirtClusterTemplateList{}
	Expect(k8sclient.List(ctx, kvClusterTmpltList, &client.ListOptions{})).To(Succeed())
	for i := range kvClusterTmpltList.Items {
		item := &kvClusterTmpltList.Items[i]
		item.SetManagedFields(nil)
	}
	Expect(
		dumpJsonFile(kvClusterTmpltList, filepath.Join(artifactsDir, "0_kubevirtclustertemplates.json")),
	).To(Succeed())

	By("dump KubevirtMachines")
	kvMachineList := &infrav1.KubevirtMachineList{}
	Expect(k8sclient.List(ctx, kvMachineList, &client.ListOptions{})).To(Succeed())
	for i := range kvMachineList.Items {
		item := &kvMachineList.Items[i]
		item.SetManagedFields(nil)
	}
	Expect(
		dumpJsonFile(kvMachineList, filepath.Join(artifactsDir, "0_kubevirtmachines.json")),
	).To(Succeed())

	By("dump KubevirtMachineTemplates")
	kvMachineTmpltList := &infrav1.KubevirtMachineTemplateList{}
	Expect(k8sclient.List(ctx, kvMachineTmpltList, &client.ListOptions{})).To(Succeed())
	for i := range kvMachineTmpltList.Items {
		item := &kvMachineTmpltList.Items[i]
		item.SetManagedFields(nil)
	}
	Expect(
		dumpJsonFile(kvMachineTmpltList, filepath.Join(artifactsDir, "0_kubevirtmachinetemplates.json")),
	).To(Succeed())
}

func dumpJsonFile(objList metav1.ListInterface, artifactsFilePath string) error {
	file, err := os.OpenFile(artifactsFilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer func() {
		if err = file.Close(); err != nil {
			GinkgoWriter.Printf("error closing file: %v\n", err)
		}
	}()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")

	return encoder.Encode(objList)
}
