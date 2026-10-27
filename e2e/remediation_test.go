package e2e_test

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	kubevirtv1 "kubevirt.io/api/core/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
)

const (
	// faultConditionType is a node condition owned by the tests. It is the
	// only thing the MachineHealthCheck of the remediation tests looks at, so
	// a test decides when a node is unhealthy and when it has recovered,
	// independently of how long the node takes to boot.
	faultConditionType = corev1.NodeConditionType("CAPKE2EFault")

	remediationTemplateName = "e2e-restart"
	remediationMHCName      = "e2e-remediation"
)

// nodeIdentity is everything that must survive a restart-based remediation.
type nodeIdentity struct {
	machineUID types.UID
	vmUID      types.UID
	pvcUIDs    map[string]types.UID
	nodeUID    types.UID
	// machineID is /etc/machine-id of the guest: it only survives if the
	// node boots from the same disk.
	machineID string
}

func postRemediationTemplate(ctx context.Context, namespace string, retryLimit, timeoutSeconds int32) {
	GinkgoHelper()

	template := &infrav1.KubevirtRemediationTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      remediationTemplateName,
			Namespace: namespace,
		},
		Spec: infrav1.KubevirtRemediationTemplateSpec{
			Template: infrav1.KubevirtRemediationTemplateResource{
				Spec: infrav1.KubevirtRemediationSpec{
					Strategy: &infrav1.RemediationStrategy{
						Type:           infrav1.RebootRemediationType,
						RetryLimit:     retryLimit,
						TimeoutSeconds: timeoutSeconds,
					},
				},
			},
		},
	}
	Expect(k8sclient.Create(ctx, template)).To(Succeed())
}

// postRemediationMHC creates a MachineHealthCheck for the worker machines of
// the cluster that remediates through the KubevirtRemediationTemplate. A
// worker is unhealthy while it reports the fault condition.
func postRemediationMHC(ctx context.Context, namespace, clusterName string) {
	GinkgoHelper()

	maxUnhealthy := intstr.FromString("100%")
	unhealthyTimeout := int32(10)
	// Disabled, so the fault condition really is the only health criterion.
	nodeStartupTimeout := int32(0)
	mhc := &clusterv1.MachineHealthCheck{
		ObjectMeta: metav1.ObjectMeta{
			Name:      remediationMHCName,
			Namespace: namespace,
		},
		Spec: clusterv1.MachineHealthCheckSpec{
			ClusterName: clusterName,
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					clusterv1.MachineDeploymentNameLabel: clusterName + "-md-0",
				},
			},
			Checks: clusterv1.MachineHealthCheckChecks{
				NodeStartupTimeoutSeconds: &nodeStartupTimeout,
				UnhealthyNodeConditions: []clusterv1.UnhealthyNodeCondition{
					{
						Type:           faultConditionType,
						Status:         corev1.ConditionTrue,
						TimeoutSeconds: &unhealthyTimeout,
					},
				},
			},
			Remediation: clusterv1.MachineHealthCheckRemediation{
				TemplateRef: clusterv1.MachineHealthCheckRemediationTemplateReference{
					APIVersion: infrav1.GroupVersion.String(),
					Kind:       "KubevirtRemediationTemplate",
					Name:       remediationTemplateName,
				},
				TriggerIf: clusterv1.MachineHealthCheckRemediationTriggerIf{
					UnhealthyLessThanOrEqualTo: &maxUnhealthy,
				},
			},
		},
	}
	Expect(k8sclient.Create(ctx, mhc)).To(Succeed())
}

// setNodeFault sets the fault condition on a tenant node. Like the conditions
// of the node problem detector, it is not owned by the kubelet and therefore
// survives a restart of the node.
func setNodeFault(ctx context.Context, tenant *kubernetes.Clientset, nodeName string, status corev1.ConditionStatus) {
	GinkgoHelper()

	now := metav1.Now().UTC().Format(time.RFC3339)
	patch := fmt.Sprintf(
		`{"status":{"conditions":[{"type":%q,"status":%q,"reason":"E2ETest","message":"set by the CAPK e2e tests","lastHeartbeatTime":%q,"lastTransitionTime":%q}]}}`,
		faultConditionType, status, now, now)

	Eventually(func() error {
		_, err := tenant.CoreV1().Nodes().PatchStatus(ctx, nodeName, []byte(patch))
		return err
	}).WithTimeout(2*time.Minute).
		WithPolling(5*time.Second).
		Should(Succeed(), "failed to set the fault condition on node %s", nodeName)
}

func setClusterPaused(ctx context.Context, namespace, clusterName string, paused bool) {
	GinkgoHelper()

	cluster := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: clusterName}}
	patch := client.RawPatch(types.MergePatchType, []byte(fmt.Sprintf(`{"spec":{"paused":%t}}`, paused)))
	Expect(k8sclient.Patch(ctx, cluster, patch)).To(Succeed())
}

// getMachineForVMI returns the Machine backed by the VMI. CAPK names the
// VirtualMachine after the KubevirtMachine.
func getMachineForVMI(ctx context.Context, vmi *kubevirtv1.VirtualMachineInstance) *clusterv1.Machine {
	GinkgoHelper()

	machineList := &clusterv1.MachineList{}
	Expect(k8sclient.List(ctx, machineList, client.InNamespace(vmi.Namespace))).To(Succeed())

	for i := range machineList.Items {
		if machineList.Items[i].Spec.InfrastructureRef.Name == vmi.Name {
			return &machineList.Items[i]
		}
	}

	Fail(fmt.Sprintf("no Machine found for VMI %s/%s", vmi.Namespace, vmi.Name))
	return nil
}

func captureNodeIdentity(ctx context.Context, tenant *kubernetes.Clientset, machine *clusterv1.Machine, vmi *kubevirtv1.VirtualMachineInstance) nodeIdentity {
	GinkgoHelper()

	vm := &kubevirtv1.VirtualMachine{}
	Expect(k8sclient.Get(ctx, client.ObjectKeyFromObject(vmi), vm)).To(Succeed())

	identity := nodeIdentity{
		machineUID: machine.UID,
		vmUID:      vm.UID,
		pvcUIDs:    map[string]types.UID{},
	}

	for _, volume := range vm.Spec.Template.Spec.Volumes {
		if volume.DataVolume == nil {
			continue
		}
		pvc := &corev1.PersistentVolumeClaim{}
		key := client.ObjectKey{Namespace: vm.Namespace, Name: volume.DataVolume.Name}
		Expect(k8sclient.Get(ctx, key, pvc)).To(Succeed())
		identity.pvcUIDs[pvc.Name] = pvc.UID
	}
	Expect(identity.pvcUIDs).ToNot(BeEmpty(), "expected VM %s to boot from a DataVolume", vm.Name)

	node := getTenantNode(ctx, tenant, vmi.Name)
	identity.nodeUID = node.UID
	identity.machineID = node.Status.NodeInfo.MachineID

	return identity
}

// getTenantNode reads a node of the tenant cluster. The request goes through
// a long-lived port-forward, so it is retried.
func getTenantNode(ctx context.Context, tenant *kubernetes.Clientset, name string) *corev1.Node {
	GinkgoHelper()

	var node *corev1.Node
	Eventually(func() error {
		var err error
		node, err = tenant.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		return err
	}).WithTimeout(2*time.Minute).
		WithPolling(5*time.Second).
		Should(Succeed(), "failed to get node %s of the tenant cluster", name)

	return node
}

// getRemediation returns the remediation request of the Machine, which the
// MachineHealthCheck controller names after it, or nil if there is none.
func getRemediation(ctx context.Context, machine *clusterv1.Machine) (*infrav1.KubevirtRemediation, error) {
	remediation := &infrav1.KubevirtRemediation{}
	err := k8sclient.Get(ctx, client.ObjectKeyFromObject(machine), remediation)
	if k8serrors.IsNotFound(err) {
		return nil, nil
	}
	return remediation, err
}

func waitForRemediation(ctx context.Context, machine *clusterv1.Machine, timeout time.Duration, description string, condition func(*infrav1.KubevirtRemediation) bool) *infrav1.KubevirtRemediation {
	GinkgoHelper()

	var remediation *infrav1.KubevirtRemediation
	Eventually(func(g Gomega) {
		var err error
		remediation, err = getRemediation(ctx, machine)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(remediation).ToNot(BeNil(), "still waiting on the MachineHealthCheck to request a remediation")
		g.Expect(condition(remediation)).To(BeTrue(), "still waiting on the remediation: %s; status: %+v", description, remediation.Status)
	}).WithTimeout(timeout).
		WithPolling(time.Second).
		Should(Succeed())

	return remediation
}

// waitForExhaustedRemediation waits for the remediation to give up, and
// reports whether the VMI was restarted on the way. The restart is observed in
// the same loop because the replacement that follows removes the VMI for good.
func waitForExhaustedRemediation(ctx context.Context, machine *clusterv1.Machine, original *kubevirtv1.VirtualMachineInstance) (remediation *infrav1.KubevirtRemediation, restarted bool) {
	GinkgoHelper()

	Eventually(func(g Gomega) {
		// The VMI is read before the request on purpose: once the request is
		// Failed the Machine is deleted, and its VMI with it.
		vmi := &kubevirtv1.VirtualMachineInstance{}
		err := k8sclient.Get(ctx, client.ObjectKeyFromObject(original), vmi)
		if k8serrors.IsNotFound(err) || (err == nil && (vmi.UID != original.UID || vmi.DeletionTimestamp != nil)) {
			restarted = true
		}

		remediation, err = getRemediation(ctx, machine)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(remediation).ToNot(BeNil(), "still waiting on the MachineHealthCheck to request a remediation")
		g.Expect(remediation.Status.Phase).To(Equal(infrav1.PhaseFailed), "still waiting on the remediation to give up; status: %+v", remediation.Status)
	}).WithTimeout(10 * time.Minute).
		WithPolling(time.Second).
		Should(Succeed())

	return remediation, restarted
}

var yamlDocumentSeparator = regexp.MustCompile(`(?m)^---\s*$`)

// useExternalInfra makes the KubevirtCluster of the cluster manifests create
// its VMs through the kubeconfig of the external infra secret.
func useExternalInfra(manifests []byte) []byte {
	GinkgoHelper()

	var out [][]byte
	found := false
	for _, doc := range yamlDocumentSeparator.Split(string(manifests), -1) {
		obj := map[string]any{}
		Expect(yaml.Unmarshal([]byte(doc), &obj)).To(Succeed())
		if len(obj) == 0 {
			continue
		}

		if obj["kind"] == "KubevirtCluster" {
			found = true
			spec, _ := obj["spec"].(map[string]any)
			if spec == nil {
				spec = map[string]any{}
			}
			spec["infraClusterSecretRef"] = map[string]any{
				"apiVersion": "v1",
				"kind":       "Secret",
				"name":       externalSecretName,
				"namespace":  externalSecretNamespace,
			}
			obj["spec"] = spec
		}

		marshalled, err := yaml.Marshal(obj)
		Expect(err).ToNot(HaveOccurred())
		out = append(out, marshalled)
	}
	Expect(found).To(BeTrue(), "no KubevirtCluster in the cluster manifests")

	return bytes.Join(out, []byte("---\n"))
}

// waitForRestartedVMI waits for the VM controller to recreate the VMI. Unlike
// getRecreatedVMI it tolerates the gap between the old and the new VMI.
func waitForRestartedVMI(ctx context.Context, namespace, name string, originalUID types.UID) *kubevirtv1.VirtualMachineInstance {
	GinkgoHelper()

	vmi := &kubevirtv1.VirtualMachineInstance{}
	Eventually(func(g Gomega) {
		g.Expect(k8sclient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, vmi)).To(Succeed())
		g.Expect(vmi.UID).ToNot(Equal(originalUID), "the original VMI is still there")
		g.Expect(vmi.DeletionTimestamp).To(BeNil())
	}).WithTimeout(8 * time.Minute).
		WithPolling(5 * time.Second).
		Should(Succeed())

	return vmi
}

// waitForNodeReadyAfter waits for the kubelet to report the node Ready at
// some point after the given time, i.e. after the node was restarted.
func waitForNodeReadyAfter(ctx context.Context, tenant *kubernetes.Clientset, nodeName string, after metav1.Time) {
	GinkgoHelper()

	Eventually(func(g Gomega) {
		node, err := tenant.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		g.Expect(err).ToNot(HaveOccurred())

		var ready *corev1.NodeCondition
		for i := range node.Status.Conditions {
			if node.Status.Conditions[i].Type == corev1.NodeReady {
				ready = &node.Status.Conditions[i]
			}
		}
		g.Expect(ready).ToNot(BeNil(), "node %s has no Ready condition", nodeName)
		g.Expect(ready.Status).To(Equal(corev1.ConditionTrue), "node %s is not Ready", nodeName)

		reportedSince := ready.LastHeartbeatTime.After(after.Time) || ready.LastTransitionTime.After(after.Time)
		g.Expect(reportedSince).To(BeTrue(), "node %s did not report its status since the restart", nodeName)
	}).WithTimeout(10 * time.Minute).
		WithPolling(10 * time.Second).
		Should(Succeed())
}

// expectNodeIdentityPreserved verifies that the node was restarted in place:
// same Machine, same VirtualMachine and disks, same Node object.
func expectNodeIdentityPreserved(ctx context.Context, tenant *kubernetes.Clientset, machine *clusterv1.Machine, vmi *kubevirtv1.VirtualMachineInstance, want nodeIdentity) {
	GinkgoHelper()

	current := &clusterv1.Machine{}
	Expect(k8sclient.Get(ctx, client.ObjectKeyFromObject(machine), current)).To(Succeed())
	Expect(current.DeletionTimestamp).To(BeNil(), "the Machine is being replaced")

	Expect(captureNodeIdentity(ctx, tenant, current, vmi)).To(Equal(want))
}

// waitForMachineReplacement waits until the Machine is gone and another worker
// Machine took its place.
func waitForMachineReplacement(ctx context.Context, original *clusterv1.Machine) {
	GinkgoHelper()

	Eventually(func(g Gomega) {
		machineList := &clusterv1.MachineList{}
		g.Expect(k8sclient.List(ctx, machineList, client.InNamespace(original.Namespace), client.MatchingLabels{
			clusterv1.MachineDeploymentNameLabel: original.Labels[clusterv1.MachineDeploymentNameLabel],
		})).To(Succeed())

		g.Expect(machineList.Items).To(HaveLen(1), "still waiting on the worker Machine to be replaced")
		g.Expect(machineList.Items[0].UID).ToNot(Equal(original.UID))
	}).WithTimeout(15 * time.Minute).
		WithPolling(5 * time.Second).
		Should(Succeed())
}
