package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	policy "k8s.io/api/policy/v1beta1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
	kubevirtv1 "kubevirt.io/api/core/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/kind/pkg/cluster/constants"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
)

const (
	testFinalizer      = "infrastructure.cluster.x-k8s.io/holdForTestFinalizer"
	calicoManifestsUrl = "https://raw.githubusercontent.com/projectcalico/calico/v3.31.4/manifests/calico.yaml"

	externalSecretName      = "external-infra-kubeconfig"
	externalSecretNamespace = "capk-system"
)

var _ = Describe("CreateCluster", func() {

	var tmpDir string
	var manifestsFile string
	var tenantKubeconfigFile string
	var namespace string
	var tenantAccessor tenantClusterAccess

	BeforeEach(func(ctx context.Context) {
		var err error

		tmpDir, err = os.MkdirTemp(WorkingDir, "creation-tests")
		Expect(err).ToNot(HaveOccurred())

		manifestsFile = filepath.Join(tmpDir, "manifests.yaml")
		tenantKubeconfigFile = filepath.Join(tmpDir, "tenant-kubeconfig.yaml")

		namespace = "e2e-test-create-cluster-" + rand.String(6)

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}

		tenantAccessor = newTenantClusterAccess(namespace, tenantKubeconfigFile)
		Expect(k8sclient.Create(ctx, ns)).To(Succeed())
	})

	AfterEach(func(ctx context.Context) {
		defer func() {
			// Best effort cleanup of remaining artifacts by deleting namespace
			By("removing namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}

			DeleteAndWait(ctx, k8sclient, ns, 120)

		}()

		Expect(os.RemoveAll(tmpDir)).To(Succeed())

		Expect(tenantAccessor.stopForwardingTenantAPI()).To(Succeed())

		By("removing cluster")
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      "kvcluster",
			},
		}
		DeleteAndWait(ctx, k8sclient, cluster, 120)

		externalInfraSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      externalSecretName,
				Namespace: externalSecretNamespace,
			},
		}

		DeleteAndWait(ctx, k8sclient, externalInfraSecret, 20)
	})

	waitForBootstrappedMachines := func(ctx context.Context) {
		Eventually(func(g Gomega) {
			machineList := &infrav1.KubevirtMachineList{}
			g.Expect(
				k8sclient.List(ctx, machineList, client.InNamespace(namespace)),
			).To(Succeed())

			g.Expect(machineList.Items).ToNot(BeEmpty(), "expecting a non-empty list of machines")

			for _, machine := range machineList.Items {
				g.Expect(conditions.IsTrue(&machine, infrav1.BootstrapExecSucceededCondition)).To(
					BeTrue(),
					"still waiting on a kubevirt machine with bootstrap succeeded condition",
				)
			}
		}).WithOffset(1).
			WithTimeout(10*time.Minute).
			WithPolling(5*time.Second).
			Should(Succeed(), "kubevirt machines should have bootstrap succeeded condition")
	}

	markExternalKubeVirtClusterReady := func(ctx context.Context, clusterName string, namespace string) {
		By("Ensuring no other controller is managing the kvcluster's status")
		Consistently(func(g Gomega) error {
			kvCluster := &infrav1.KubevirtCluster{}
			key := client.ObjectKey{Namespace: namespace, Name: clusterName}
			g.Expect(k8sclient.Get(ctx, key, kvCluster)).To(Succeed())

			g.Expect(kvCluster.Finalizers).To(BeEmpty())
			g.Expect(kvCluster.Status.Ready).To(BeFalse())
			g.Expect(kvCluster.Status.FailureDomains).To(BeEmpty())
			g.Expect(kvCluster.Status.Conditions).To(BeEmpty())

			return nil
		}, 30*time.Second, 5*time.Second).Should(Succeed())

		By("Setting creating load balancer on kvcluster object")

		lbService := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      clusterName + "-lb",
			},
			Spec: corev1.ServiceSpec{
				Type: corev1.ServiceTypeClusterIP,
				Ports: []corev1.ServicePort{
					{
						Port:       6443,
						Protocol:   corev1.ProtocolTCP,
						TargetPort: intstr.FromInt32(6443),
					},
				},
				Selector: map[string]string{
					"cluster.x-k8s.io/role":         constants.ControlPlaneNodeRoleValue,
					"cluster.x-k8s.io/cluster-name": clusterName,
				},
			},
		}
		Expect(k8sclient.Create(ctx, lbService)).To(Succeed())

		By("getting IP of load balancer")

		lbIP := ""
		Eventually(func(g Gomega) {
			updatedLB := &corev1.Service{}
			key := client.ObjectKey{Namespace: namespace, Name: lbService.Name}
			g.Expect(k8sclient.Get(ctx, key, updatedLB)).To(Succeed())
			g.Expect(updatedLB.Spec.ClusterIP).ToNot(BeEmpty(), "still waiting on lb ip")
			lbIP = updatedLB.Spec.ClusterIP
		}, 30*time.Second, 5*time.Second).Should(Succeed(), "lb should have provided an ip")

		By("Setting ready=true on kvcluster object")
		kvCluster := &infrav1.KubevirtCluster{}
		key := client.ObjectKey{Namespace: namespace, Name: clusterName}
		Expect(k8sclient.Get(ctx, key, kvCluster)).To(Succeed())

		kvCluster.Spec.ControlPlaneEndpoint = infrav1.APIEndpoint{
			Host: lbIP,
			Port: 6443,
		}
		Expect(k8sclient.Update(ctx, kvCluster)).To(Succeed())

		conditions.Set(kvCluster, metav1.Condition{
			Type:   infrav1.LoadBalancerAvailableCondition,
			Status: metav1.ConditionTrue,
			Reason: "LoadBalancerAvailable",
		})
		kvCluster.Status.Ready = true

		Expect(k8sclient.Status().Update(ctx, kvCluster)).To(Succeed())

		Eventually(func(g Gomega, ctx context.Context) bool {
			g.Expect(k8sclient.Get(ctx, key, kvCluster)).To(Succeed())
			return kvCluster.Status.Ready
		}).WithContext(ctx).
			WithTimeout(5*time.Minute).
			WithPolling(10*time.Second).
			Should(BeTrueBecause("kvCluster.Status.Ready should be true"), printObjFunc(kvCluster))
	}

	waitForMachineReadiness := func(ctx context.Context, numExpectedReady int, numExpectedNotReady int) {
		Eventually(func(g Gomega) {
			readyCount := 0
			notReadyCount := 0

			machineList := &infrav1.KubevirtMachineList{}
			g.Expect(k8sclient.List(ctx, machineList, client.InNamespace(namespace))).To(Succeed())

			g.Expect(machineList.Items).ToNot(BeEmpty(), "expecting a non-empty list of machines")

			for _, machine := range machineList.Items {
				if machine.Status.Ready {
					readyCount++
				} else {
					notReadyCount++
				}
			}
			g.Expect(readyCount).To(Equal(numExpectedReady), "Expected %d ready, but got %d", numExpectedReady, readyCount)
			g.Expect(notReadyCount).To(Equal(numExpectedNotReady), "Expected %d not ready, but got %d", numExpectedNotReady, notReadyCount)
		}).WithOffset(1).
			WithTimeout(10*time.Minute).
			WithPolling(5*time.Second).
			Should(Succeed(), "waiting for expected readiness.")
	}

	waitForTenantPods := func(ctx context.Context) {

		By(fmt.Sprintf("Perform Port Forward using controlplane vmi in namespace %s", namespace))
		Expect(tenantAccessor.startForwardingTenantAPI(ctx, k8sclient)).To(Succeed())

		By("Create client to access the tenant cluster")
		clientSet, err := tenantAccessor.generateClient()
		Expect(err).ToNot(HaveOccurred())

		Eventually(func(g Gomega) []string {
			podList, err := clientSet.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{})
			g.Expect(err).ToNot(HaveOccurred())

			var offlinePodList []string
			for _, pod := range podList.Items {
				if pod.Status.Phase != corev1.PodRunning {
					offlinePodList = append(offlinePodList, pod.Name)
				}
			}

			return offlinePodList
		}).WithOffset(1).
			WithTimeout(8*time.Minute).
			WithPolling(5*time.Second).
			Should(BeEmpty(), "waiting for pods to hit Running phase.")

	}

	waitForTenantAccess := func(ctx context.Context, numExpectedNodes int) *kubernetes.Clientset {
		GinkgoHelper()

		By(fmt.Sprintf("Perform Port Forward using controlplane vmi in namespace %s", namespace))
		Expect(tenantAccessor.startForwardingTenantAPI(ctx, k8sclient)).To(Succeed())

		By("Create client to access the tenant cluster")
		clientSet, err := tenantAccessor.generateClient()
		Expect(err).ToNot(HaveOccurred())

		Eventually(func(g Gomega) []corev1.Node {

			nodeList, err := clientSet.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			g.Expect(err).ToNot(HaveOccurred())
			return nodeList.Items
		}).WithTimeout(10*time.Minute).
			WithPolling(10*time.Second).
			Should(HaveLen(numExpectedNodes), "waiting for expected readiness.")

		return clientSet
	}

	waitForNodeReadiness := func(ctx context.Context) *kubernetes.Clientset {
		GinkgoHelper()

		By(fmt.Sprintf("Perform Port Forward using controlplane vmi in namespace %s", namespace))
		Expect(tenantAccessor.startForwardingTenantAPI(ctx, k8sclient)).To(Succeed())

		By("Create client to access the tenant cluster")
		clientSet, err := tenantAccessor.generateClient()
		Expect(err).ToNot(HaveOccurred())

		Eventually(func(g Gomega) error {

			nodeList, err := clientSet.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			g.Expect(err).ToNot(HaveOccurred())

			for _, node := range nodeList.Items {

				ready := false
				networkAvailable := false
				for _, cond := range node.Status.Conditions {
					if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
						ready = true
					} else if cond.Type == corev1.NodeNetworkUnavailable && cond.Status == corev1.ConditionFalse {
						networkAvailable = true
					}
				}

				if !ready {
					return fmt.Errorf("waiting on node %s to become ready", node.Name)
				} else if !networkAvailable {
					return fmt.Errorf("waiting on node %s to have network availablity", node.Name)
				}
			}

			return nil
		}).WithTimeout(10*time.Minute).
			WithPolling(10*time.Second).
			Should(Succeed(), "ensure healthy nodes.")

		return clientSet
	}

	installCalicoCNI := func() {
		cmd := exec.Command(KubectlPath, "--kubeconfig", tenantKubeconfigFile, "--insecure-skip-tls-verify", "--validate=false", "--server", fmt.Sprintf("https://localhost:%d", tenantAccessor.tenantApiPort), "apply", "-f", calicoManifestsUrl)
		_, stderr := RunCmd(cmd)
		if len(stderr) > 0 {
			GinkgoLogr.Info("Warning", "clusterctl's stderr", string(stderr))
		}
	}

	waitForNodeUpdate := func(ctx context.Context) {
		Eventually(func(g Gomega) {
			machineList := &infrav1.KubevirtMachineList{}
			g.Expect(
				k8sclient.List(ctx, machineList, client.InNamespace(namespace)),
			).To(Succeed())

			for _, machine := range machineList.Items {
				g.Expect(machine.Status.NodeUpdated).
					To(
						BeTrue(),
						"Still waiting on machine %s to have node provider id updated", machine.Name,
					)
			}
		}, 5*time.Minute, 5*time.Second).Should(Succeed(), "waiting for expected readiness.")
	}

	waitForControlPlane := func(ctx context.Context) {
		By("Waiting on cluster's control plane to initialize")
		Eventually(func(g Gomega) {
			cluster := &clusterv1.Cluster{}
			key := client.ObjectKey{Namespace: namespace, Name: "kvcluster"}
			g.Expect(k8sclient.Get(ctx, key, cluster)).To(Succeed())
			g.Expect(conditions.IsTrue(cluster, clusterv1.ClusterControlPlaneInitializedCondition)).To(
				BeTrue(),
				"still waiting on controlPlaneReady condition to be true",
			)
		}).WithOffset(1).
			WithTimeout(20*time.Minute).
			WithPolling(5*time.Second).
			Should(Succeed(), "cluster should have control plane initialized")

		By("Waiting on cluster's control plane to be ready")
		Eventually(func(g Gomega) {
			cluster := &clusterv1.Cluster{}
			key := client.ObjectKey{Namespace: namespace, Name: "kvcluster"}
			g.Expect(k8sclient.Get(ctx, key, cluster)).To(Succeed())
			g.Expect(conditions.IsTrue(cluster, clusterv1.ClusterControlPlaneAvailableCondition)).To(
				BeTrue(),
				"still waiting on controlPlaneInitialized condition to be true",
			)
		}).WithOffset(1).
			WithTimeout(15*time.Minute).
			WithPolling(5*time.Second).
			Should(Succeed(), "cluster should have control plane initialized")
	}

	injectKubevirtClusterExternallyManagedAnnotation := func(yamlStr string) string {
		strs := strings.Split(yamlStr, "\n")

		labelInjection := `  annotations:
    cluster.x-k8s.io/managed-by: external
`
		newString := ""
		for i, s := range strs {
			if strings.Contains(s, "metadata:") && i > 0 && strings.Contains(strs[i-1], "kind: KubevirtCluster") {
				newString = newString + s + "\n" + labelInjection
			} else {
				newString = newString + s + "\n"
			}
		}

		return newString
	}

	chooseWorkerVMI := func(ctx context.Context) *kubevirtv1.VirtualMachineInstance {
		vmiList := &kubevirtv1.VirtualMachineInstanceList{}
		Expect(k8sclient.List(ctx, vmiList, client.InNamespace(namespace))).To(Succeed())

		var chosenVMI *kubevirtv1.VirtualMachineInstance
		for _, vmi := range vmiList.Items {
			if strings.Contains(vmi.Name, "-md-0") {
				chosenVMI = &vmi
				break
			}
		}
		Expect(chosenVMI).ToNot(BeNil())
		return chosenVMI
	}

	getVMIPod := func(ctx context.Context, vmi *kubevirtv1.VirtualMachineInstance) *corev1.Pod {

		podList := &corev1.PodList{}
		Expect(k8sclient.List(ctx, podList, client.InNamespace(namespace))).To(Succeed())

		var chosenPod *corev1.Pod
		for _, pod := range podList.Items {
			if pod.Status.Phase != corev1.PodRunning {
				continue
			}

			vmName, ok := pod.Labels["kubevirt.io/vm"]
			if !ok {
				continue
			} else if vmName == vmi.Name {
				chosenPod = &pod
				break
			}
		}
		Expect(chosenPod).ToNot(BeNil())
		return chosenPod
	}

	waitForRemoval := func(ctx context.Context, obj client.Object, timeoutSeconds uint) {
		GinkgoHelper()
		key := client.ObjectKeyFromObject(obj)
		Eventually(func() error {
			err := k8sclient.Get(ctx, key, obj)
			if err != nil && k8serrors.IsNotFound(err) {
				return nil
			} else if err != nil {
				return err
			}

			return fmt.Errorf("waiting on object %s to be deleted", key)
		}).WithTimeout(time.Duration(timeoutSeconds)*time.Second).
			WithPolling(1*time.Second).
			Should(Succeed(), printObjFunc(obj))
	}

	postDefaultMHC := func(ctx context.Context, clusterName string) {
		maxUnhealthy := intstr.FromString("100%")
		nodeStartupTimeout := int32(600)
		unhealthyTimeout := int32(300)
		mhc := &clusterv1.MachineHealthCheck{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "testmhc",
				Namespace: namespace,
			},
			Spec: clusterv1.MachineHealthCheckSpec{
				ClusterName: clusterName,
				Selector: metav1.LabelSelector{
					MatchLabels: map[string]string{
						"cluster.x-k8s.io/cluster-name": clusterName,
					},
				},
				Checks: clusterv1.MachineHealthCheckChecks{
					NodeStartupTimeoutSeconds: &nodeStartupTimeout,
					UnhealthyNodeConditions: []clusterv1.UnhealthyNodeCondition{
						{
							Type:           corev1.NodeReady,
							Status:         corev1.ConditionFalse,
							TimeoutSeconds: &unhealthyTimeout,
						},
						{
							Type:           corev1.NodeReady,
							Status:         corev1.ConditionUnknown,
							TimeoutSeconds: &unhealthyTimeout,
						},
					},
				},
				Remediation: clusterv1.MachineHealthCheckRemediation{
					TriggerIf: clusterv1.MachineHealthCheckRemediationTriggerIf{
						UnhealthyLessThanOrEqualTo: &maxUnhealthy,
					},
				},
			},
		}

		Expect(k8sclient.Create(ctx, mhc)).To(Succeed())
	}

	validationVMTemplate := func() infrav1.VirtualMachineTemplateSpec {
		return infrav1.VirtualMachineTemplateSpec{
			Spec: kubevirtv1.VirtualMachineSpec{
				Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{},
			},
		}
	}

	It("rejects invalid cloud-init network data configuration", Label("apiValidation"), func(ctx context.Context) {
		machine := &infrav1.KubevirtMachine{
			ObjectMeta: metav1.ObjectMeta{Name: "cloud-init-validation", Namespace: namespace},
			Spec: infrav1.KubevirtMachineSpec{
				VirtualMachineTemplate: validationVMTemplate(),
				CloudInit: &infrav1.CloudInitSpec{
					NetworkDataSecretRef: &corev1.LocalObjectReference{},
				},
			},
		}
		err := k8sclient.Create(ctx, machine)
		Expect(k8serrors.IsInvalid(err)).To(BeTrue(), "expected API validation rejection, got %v", err)
		Expect(err.Error()).To(ContainSubstring("networkDataSecretRef.name must not be empty"))

		machine.Spec.CloudInit.NetworkDataSecretRef.Name = "network-data"
		machine.Spec.CloudInit.NetworkData = ptr.To("version: 2")
		err = k8sclient.Create(ctx, machine)
		Expect(k8serrors.IsInvalid(err)).To(BeTrue(), "expected API validation rejection, got %v", err)
		Expect(err.Error()).To(ContainSubstring("networkData and networkDataSecretRef are mutually exclusive"))
	})

	DescribeTable("validates cloud-init immutability through the API", Label("apiValidation"),
		func(ctx context.Context, initial *infrav1.CloudInitSpec, updated *infrav1.CloudInitSpec, rejected bool) {
			machine := &infrav1.KubevirtMachine{
				ObjectMeta: metav1.ObjectMeta{Name: "cloud-init-validation", Namespace: namespace},
				Spec: infrav1.KubevirtMachineSpec{
					VirtualMachineTemplate: validationVMTemplate(),
					CloudInit:              initial.DeepCopy(),
				},
			}
			Expect(k8sclient.Create(ctx, machine)).To(Succeed())
			original := machine.DeepCopy()
			machine.Spec.CloudInit = updated.DeepCopy()
			machine.Spec.ProviderID = ptr.To("kubevirt://updated")
			machine.Labels = map[string]string{"test": "updated"}
			err := k8sclient.Update(ctx, machine)
			if rejected {
				Expect(k8serrors.IsInvalid(err)).To(BeTrue(), "expected immutability rejection, got %v", err)
				Expect(err.Error()).To(ContainSubstring("CloudInit"))
			} else {
				Expect(err).ToNot(HaveOccurred())
			}
			persisted := &infrav1.KubevirtMachine{}
			Expect(k8sclient.Get(ctx, client.ObjectKeyFromObject(machine), persisted)).To(Succeed())
			if rejected {
				Expect(persisted.Spec).To(Equal(original.Spec))
			} else {
				Expect(persisted.Spec).To(Equal(machine.Spec))
				Expect(persisted.Labels).To(HaveKeyWithValue("test", "updated"))
			}
		},
		Entry("rejects adding cloudInit", nil,
			&infrav1.CloudInitSpec{DataSource: infrav1.CloudInitDataSourceNoCloud}, true),
		Entry("rejects removing cloudInit",
			&infrav1.CloudInitSpec{DataSource: infrav1.CloudInitDataSourceNoCloud}, nil, true),
		Entry("rejects changing cloudInit",
			&infrav1.CloudInitSpec{DataSource: infrav1.CloudInitDataSourceConfigDrive},
			&infrav1.CloudInitSpec{DataSource: infrav1.CloudInitDataSourceNoCloud}, true),
		Entry("allows unrelated updates without cloudInit", nil, nil, false),
		Entry("allows unrelated updates with cloudInit",
			&infrav1.CloudInitSpec{DataSource: infrav1.CloudInitDataSourceNoCloud},
			&infrav1.CloudInitSpec{DataSource: infrav1.CloudInitDataSourceNoCloud}, false),
	)

	It("allows topology dry-run cloud-init template changes but rejects persisted changes", Label("apiValidation"), func(ctx context.Context) {
		template := &infrav1.KubevirtMachineTemplate{
			ObjectMeta: metav1.ObjectMeta{Name: "cloud-init-template-validation", Namespace: namespace},
			Spec: infrav1.KubevirtMachineTemplateSpec{
				Template: infrav1.KubevirtMachineTemplateResource{
					Spec: infrav1.KubevirtMachineSpec{
						VirtualMachineTemplate: validationVMTemplate(),
						CloudInit:              &infrav1.CloudInitSpec{DataSource: infrav1.CloudInitDataSourceConfigDrive},
					},
				},
			},
		}
		Expect(k8sclient.Create(ctx, template)).To(Succeed())
		original := template.DeepCopy()
		updated := template.DeepCopy()
		updated.Spec.Template.Spec.CloudInit.DataSource = infrav1.CloudInitDataSourceNoCloud
		updated.Spec.Template.Spec.CloudInit.NetworkData = ptr.To("version: 2")
		Expect(k8sclient.Update(ctx, updated, client.DryRunAll)).To(Succeed())
		Expect(k8sclient.Get(ctx, client.ObjectKeyFromObject(template), template)).To(Succeed())
		Expect(template.Spec).To(Equal(original.Spec))
		err := k8sclient.Update(ctx, updated)
		Expect(k8serrors.IsForbidden(err)).To(BeTrue(), "expected webhook rejection, got %v", err)
		Expect(err.Error()).To(ContainSubstring("KubevirtMachineTemplateSpec is immutable"))
	})

	DescribeTable("creates a simple cluster with ephemeral VMs and cloud-init network data", Label("ephemeralVMs"),
		func(ctx context.Context, dataSource infrav1.CloudInitDataSource) {
			By("generating cluster manifests from example template")
			cmd := exec.Command(ClusterctlPath, "generate", "cluster", "kvcluster",
				"--target-namespace", namespace,
				"--kubernetes-version", os.Getenv("TENANT_CLUSTER_KUBERNETES_VERSION"),
				"--control-plane-machine-count=1",
				"--worker-machine-count=1",
				"--from", "templates/cluster-template.yaml")
			stdout, stderr := RunCmd(cmd)
			if len(stderr) > 0 {
				GinkgoLogr.Info("Warning", "clusterctl's stderr", string(stderr))
			}

			manifests := string(stdout)
			bootstrapCheck := "      virtualMachineBootstrapCheck:\n"
			Expect(strings.Count(manifests, bootstrapCheck)).To(Equal(2))
			const dnsServer = "1.1.1.1"
			const macAddress = "02:00:00:00:00:01"
			// Bridge binding gives each guest a distinct, routable pod IP for the tenant CNI.
			// ConfigDrive identifies the link by MAC; each interface is isolated in its own pod.
			disks := "                  disks:\n"
			Expect(strings.Count(manifests, disks)).To(Equal(2))
			interfaces := "                  interfaces:\n                    - name: default\n                      bridge: {}\n                      macAddress: " + macAddress + "\n"
			manifests = strings.ReplaceAll(manifests, disks, interfaces+disks)
			volumes := "              volumes:\n"
			Expect(strings.Count(manifests, volumes)).To(Equal(2))
			networks := "              networks:\n                - name: default\n                  pod: {}\n"
			manifests = strings.ReplaceAll(manifests, volumes, networks+volumes)
			networkData := "version: 2\nethernets:\n  default:\n    match:\n      name: \"*\"\n    dhcp4: true\n    nameservers:\n      addresses: [" + dnsServer + "]\n"
			cloudInitConfig := "      cloudInit:\n        dataSource: noCloud\n        networkData: |\n" +
				"          " + strings.ReplaceAll(strings.TrimSpace(networkData), "\n", "\n          ") + "\n"
			if dataSource == infrav1.CloudInitDataSourceConfigDrive {
				By("creating a Secret with OpenStack ConfigDrive network data")
				// Ubuntu's cloud-init v1-to-netplan renderer only emits DNS for static subnets.
				// Keep DHCP for IPv4 connectivity and attach DNS to a documentation-only IPv6 address.
				networkData = fmt.Sprintf(`{
  "links": [{"id": "default", "type": "phy", "ethernet_mac_address": %q}],
  "networks": [
    {"id": "default", "type": "ipv4_dhcp", "link": "default"},
    {"id": "dns", "type": "ipv6", "link": "default",
     "ip_address": "2001:db8::10", "netmask": 128,
     "services": [{"type": "dns", "address": %q}]}
  ],
  "services": []
}`, macAddress, dnsServer)
				networkSecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "cloud-init-network-data", Namespace: namespace},
					Data:       map[string][]byte{"networkData": []byte(networkData)},
				}
				Expect(k8sclient.Create(ctx, networkSecret)).To(Succeed())
				cloudInitConfig = "      cloudInit:\n        dataSource: configDrive\n        networkDataSecretRef:\n          name: " + networkSecret.Name + "\n"
			}
			manifests = strings.ReplaceAll(manifests, bootstrapCheck, cloudInitConfig+bootstrapCheck)

			Expect(os.WriteFile(manifestsFile, []byte(manifests), 0644)).To(Succeed())

			By("posting cluster manifests example template")
			cmd = exec.Command(KubectlPath, "apply", "-f", manifestsFile)
			RunCmd(cmd)

			By("Waiting for control plane")
			waitForControlPlane(ctx)

			By("Waiting on kubevirt machines to bootstrap")
			waitForBootstrappedMachines(ctx)

			By("checking network data shares the bootstrap secret")
			vmList := &kubevirtv1.VirtualMachineList{}
			Expect(k8sclient.List(ctx, vmList, client.InNamespace(namespace))).To(Succeed())
			Expect(vmList.Items).To(HaveLen(2))
			for i := range vmList.Items {
				var userDataRef, networkDataRef *corev1.LocalObjectReference
				for _, volume := range vmList.Items[i].Spec.Template.Spec.Volumes {
					if dataSource == infrav1.CloudInitDataSourceNoCloud && volume.CloudInitNoCloud != nil {
						userDataRef = volume.CloudInitNoCloud.UserDataSecretRef
						networkDataRef = volume.CloudInitNoCloud.NetworkDataSecretRef
					}
					if dataSource == infrav1.CloudInitDataSourceConfigDrive && volume.CloudInitConfigDrive != nil {
						userDataRef = volume.CloudInitConfigDrive.UserDataSecretRef
						networkDataRef = volume.CloudInitConfigDrive.NetworkDataSecretRef
					}
				}
				Expect(userDataRef).ToNot(BeNil())
				Expect(networkDataRef).ToNot(BeNil())
				Expect(networkDataRef.Name).To(Equal(userDataRef.Name))

				cloudInitSecret := &corev1.Secret{}
				secretKey := client.ObjectKey{Namespace: namespace, Name: userDataRef.Name}
				Expect(k8sclient.Get(ctx, secretKey, cloudInitSecret)).To(Succeed())
				Expect(cloudInitSecret.Data["networkdata"]).To(Equal([]byte(networkData)))
			}

			By("verifying every guest applied the supplied DNS server")
			kvCluster := &infrav1.KubevirtCluster{}
			Expect(k8sclient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "kvcluster"}, kvCluster)).To(Succeed())
			Expect(kvCluster.Spec.SshKeys.DataSecretName).ToNot(BeNil())
			sshSecret := &corev1.Secret{}
			Expect(k8sclient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: *kvCluster.Spec.SshKeys.DataSecretName}, sshSecret)).To(Succeed())
			Expect(sshSecret.Data["key"]).ToNot(BeEmpty())
			identityFile := filepath.Join(tmpDir, "guest-ssh-key")
			Expect(os.WriteFile(identityFile, sshSecret.Data["key"], 0600)).To(Succeed())
			for _, vm := range vmList.Items {
				Eventually(func(g Gomega) {
					sshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
					defer cancel()
					cmd := exec.CommandContext(sshCtx, VirtctlPath, "ssh", "-n", namespace,
						"--identity-file="+identityFile,
						"--local-ssh-opts=-o StrictHostKeyChecking=no",
						"--local-ssh-opts=-o UserKnownHostsFile=/dev/null",
						"--local-ssh-opts=-o BatchMode=yes",
						"--local-ssh-opts=-o ConnectTimeout=10",
						"--command=resolvectl dns", "capk@vmi/"+vm.Name)
					output, err := cmd.CombinedOutput()
					g.Expect(err).ToNot(HaveOccurred(), "guest %s: %s", vm.Name, output)
					g.Expect(string(output)).To(MatchRegexp(`(?:^|\s)`+regexp.QuoteMeta(dnsServer)+`(?:\s|$)`),
						"guest %s did not apply cloud-init DNS configuration", vm.Name)
				}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())
			}

			By("Waiting on kubevirt machines to be ready")
			waitForMachineReadiness(ctx, 2, 0)

			By("Waiting for getting access to the tenant cluster")
			waitForTenantAccess(ctx, 2)

			By("posting calico CNI manifests to the guest cluster and waiting for network")
			installCalicoCNI()

			By("Waiting for node readiness")
			waitForNodeReadiness(ctx)

			By("waiting all tenant Pods to be Ready")
			waitForTenantPods(ctx)
		},
		Entry("inline NoCloud", infrav1.CloudInitDataSourceNoCloud),
		Entry("Secret-backed ConfigDrive", infrav1.CloudInitDataSourceConfigDrive),
	)

	It("should remediate a running VMI marked as being in a terminal state", Label("ephemeralVMs", "terminal"), func(ctx context.Context) {
		By("generating cluster manifests from example template")
		cmd := exec.Command(ClusterctlPath, "generate", "cluster", "kvcluster",
			"--target-namespace", namespace,
			"--kubernetes-version", os.Getenv("TENANT_CLUSTER_KUBERNETES_VERSION"),
			"--control-plane-machine-count=1",
			"--worker-machine-count=1",
			"--from", "templates/cluster-template.yaml")
		stdout, _ := RunCmd(cmd)
		Expect(os.WriteFile(manifestsFile, stdout, 0644)).To(Succeed())

		By("posting cluster manifests example template")
		cmd = exec.Command(KubectlPath, "apply", "-f", manifestsFile)
		RunCmd(cmd)

		By("Waiting for control plane")
		waitForControlPlane(ctx)

		By("Waiting on kubevirt machines to bootstrap")
		waitForBootstrappedMachines(ctx)

		By("Waiting on kubevirt machines to be ready")
		waitForMachineReadiness(ctx, 2, 0)

		By("Waiting for getting access to the tenant cluster")
		waitForTenantAccess(ctx, 2)

		By("creating machine health check")
		postDefaultMHC(ctx, "kvcluster")

		// trigger remediation by marking a running VMI as being in a failed state
		By("Selecting a worker node to remediate")
		chosenVMI := chooseWorkerVMI(ctx)

		By("Setting terminal state on VMI")
		kvmName, ok := chosenVMI.Labels["capk.cluster.x-k8s.io/kubevirt-machine-name"]
		Expect(ok).To(BeTrue())

		chosenVMI.Labels[infrav1.KubevirtMachineVMTerminalLabel] = "marked-terminal-by-func-test"
		Expect(k8sclient.Update(ctx, chosenVMI)).To(Succeed())

		By("Wait for KubeVirtMachine is deleted due to remediation")
		chosenKVM := &infrav1.KubevirtMachine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      kvmName,
				Namespace: chosenVMI.Namespace,
			},
		}
		waitForRemoval(ctx, chosenKVM, 600)

		By("Waiting on kubevirt new machines to be ready after remediation")
		waitForMachineReadiness(ctx, 2, 0)

		// stop port forwarding before starting new one (in waitForTenantAccess)
		Expect(tenantAccessor.stopForwardingTenantAPI()).To(Succeed())

		By("Waiting for getting access to the tenant cluster")
		waitForTenantAccess(ctx, 2)

		By("posting calico CNI manifests to the guest cluster and waiting for network")
		installCalicoCNI()

		By("Waiting for node readiness")
		waitForNodeReadiness(ctx)

		By("waiting all tenant Pods to be Ready")
		waitForTenantPods(ctx)

	})

	It("should remediate failed unrecoverable VMI ", Label("ephemeralVMs"), func(ctx context.Context) {

		By("generating cluster manifests from example template")
		cmd := exec.Command(ClusterctlPath, "generate", "cluster", "kvcluster",
			"--target-namespace", namespace,
			"--kubernetes-version", os.Getenv("TENANT_CLUSTER_KUBERNETES_VERSION"),
			"--control-plane-machine-count=1",
			"--worker-machine-count=1",
			"--from", "templates/cluster-template.yaml")
		stdout, _ := RunCmd(cmd)
		Expect(os.WriteFile(manifestsFile, stdout, 0644)).To(Succeed())

		By("posting cluster manifests example template")
		cmd = exec.Command(KubectlPath, "apply", "-f", manifestsFile)
		RunCmd(cmd)

		By("Waiting for control plane")
		waitForControlPlane(ctx)

		By("Waiting on kubevirt machines to bootstrap")
		waitForBootstrappedMachines(ctx)

		By("Waiting on kubevirt machines to be ready")
		waitForMachineReadiness(ctx, 2, 0)

		By("Waiting for getting access to the tenant cluster")
		waitForTenantAccess(ctx, 2)

		By("creating machine health check")
		postDefaultMHC(ctx, "kvcluster")

		// trigger remediation by putting the VMI in a permanent stopped state
		By("Selecting new worker node to remediate")
		chosenVMI := chooseWorkerVMI(ctx)

		By("Setting VM to runstrategy once")
		key := client.ObjectKeyFromObject(chosenVMI)
		chosenVM := &kubevirtv1.VirtualMachine{}
		Expect(k8sclient.Get(ctx, key, chosenVM)).To(Succeed())

		chosenVM.Spec.RunStrategy = ptr.To(kubevirtv1.RunStrategyOnce)
		Expect(k8sclient.Update(ctx, chosenVM)).To(Succeed())

		By("killing the chosen VMI's pod")
		chosenPod := getVMIPod(ctx, chosenVMI)
		DeleteAndWait(ctx, k8sclient, chosenPod, 180)

		By("Wait for KubeVirtMachine is deleted due to remediation")
		kvmName, ok := chosenVMI.Labels["capk.cluster.x-k8s.io/kubevirt-machine-name"]
		Expect(ok).To(BeTrue())
		chosenKVM := &infrav1.KubevirtMachine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      kvmName,
				Namespace: chosenVMI.Namespace,
			},
		}
		waitForRemoval(ctx, chosenKVM, 10*60)

		// stop port forwarding before starting new one (in waitForTenantAccess)
		Expect(tenantAccessor.stopForwardingTenantAPI()).To(Succeed())

		By("Waiting on kubevirt new machines to be ready after remediation")
		waitForMachineReadiness(ctx, 2, 0)

		By("Waiting for getting access to the tenant cluster")
		waitForTenantAccess(ctx, 2)

	})

	It("creates a simple externally managed cluster ephemeral VMs", Label("ephemeralVMs", "externallyManaged"), func(ctx context.Context) {
		By("generating cluster manifests from example template")
		cmd := exec.Command(ClusterctlPath, "generate", "cluster", "kvcluster",
			"--target-namespace", namespace,
			"--kubernetes-version", os.Getenv("TENANT_CLUSTER_KUBERNETES_VERSION"),
			"--control-plane-machine-count=1",
			"--worker-machine-count=1",
			"--from", "templates/cluster-template.yaml")
		stdout, _ := RunCmd(cmd)

		modifiedStdOut := injectKubevirtClusterExternallyManagedAnnotation(string(stdout))

		Expect(os.WriteFile(manifestsFile, []byte(modifiedStdOut), 0644)).To(Succeed())

		By("posting cluster manifests example template")
		cmd = exec.Command(KubectlPath, "apply", "-f", manifestsFile)
		RunCmd(cmd)

		By("marking kubevirt cluster as ready to imitate an externally managed cluster")
		markExternalKubeVirtClusterReady(ctx, "kvcluster", namespace)

		By("Waiting for control plane")
		waitForControlPlane(ctx)

		By("Waiting on kubevirt machines to be ready")
		waitForMachineReadiness(ctx, 2, 0)

		By("Waiting for getting access to the tenant cluster")
		waitForTenantAccess(ctx, 2)

		By("Waiting for all tenant nodes to get provider id")
		waitForNodeUpdate(ctx)

		By("Ensuring cluster teardown works without race conditions by deleting namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		DeleteAndWait(ctx, k8sclient, ns, 120)
	})

	It("creates a simple cluster with persistent VMs, then evict the node", Label("persistentVMs", "eviction"), func(ctx context.Context) {
		By("generating cluster manifests from example template")
		cmd := exec.Command(ClusterctlPath, "generate", "cluster", "kvcluster",
			"--target-namespace", namespace,
			"--kubernetes-version", os.Getenv("TENANT_CLUSTER_KUBERNETES_VERSION"),
			"--control-plane-machine-count=1",
			"--worker-machine-count=1",
			"--from", "templates/cluster-template-persistent-storage.yaml")
		stdout, _ := RunCmd(cmd)
		Expect(os.WriteFile(manifestsFile, stdout, 0644)).To(Succeed())

		By("posting cluster manifests example template")
		cmd = exec.Command(KubectlPath, "apply", "-f", manifestsFile)
		RunCmd(cmd)

		By("Waiting for control plane")
		waitForControlPlane(ctx)

		By("Waiting on kubevirt machines to bootstrap")
		waitForBootstrappedMachines(ctx)

		By("Waiting on kubevirt machines to be ready")
		waitForMachineReadiness(ctx, 2, 0)

		By("Waiting for getting access to the tenant cluster")
		waitForTenantAccess(ctx, 2)

		By("Selecting a worker node to restart")
		vmiList := &kubevirtv1.VirtualMachineInstanceList{}
		Expect(k8sclient.List(ctx, vmiList, client.InNamespace(namespace))).To(Succeed())

		var chosenVMI *kubevirtv1.VirtualMachineInstance
		for _, vmi := range vmiList.Items {
			if strings.Contains(vmi.Name, "-md-0") {
				chosenVMI = &vmi
				break
			}
		}
		Expect(chosenVMI).ToNot(BeNil())

		By("Set a testFinalizer to hold the VMI deletion so we could check if the machine became not-ready")
		addFinalizerFromVMI(ctx, k8sclient, chosenVMI.Name, namespace)

		By(fmt.Sprintf("By restarting worker node hosted in vmi %s", chosenVMI.Name))
		Expect(k8sclient.Delete(ctx, chosenVMI)).To(Succeed())

		By("Expecting a KubevirtMachine to revert back to ready=false while VM restarts")
		waitForMachineReadiness(ctx, 1, 1)

		deletedVmi := getVmiByKey(ctx, k8sclient, client.ObjectKeyFromObject(chosenVMI))
		removeFinalizerFromVMI(ctx, k8sclient, deletedVmi)

		By("Expecting both KubevirtMachines stabilize to a ready=true again.")
		waitForMachineReadiness(ctx, 2, 0)

		By("Re-establishing port-forward after VMI restart")
		Expect(tenantAccessor.stopForwardingTenantAPI()).To(Succeed())

		By("Waiting for getting access to the tenant cluster")
		clientSet := waitForTenantAccess(ctx, 2)

		By("posting calico CNI manifests to the guest cluster and waiting for network")
		installCalicoCNI()

		By("Waiting for node readiness")
		waitForNodeReadiness(ctx)

		By("waiting all tenant Pods to be Ready")
		waitForTenantPods(ctx)

		vmiName := chosenVMI.Name

		By("read the VMI again after it was recreated")
		recreatedVMI := getRecreatedVMI(ctx, k8sclient, vmiName, namespace, chosenVMI.GetUID())

		Expect(*recreatedVMI.Spec.EvictionStrategy).Should(Equal(kubevirtv1.EvictionStrategyExternal))

		By("Set a testFinalizer to hold the VMI deletion so we could query it after eviction")
		recreatedVMI = addFinalizerFromVMI(ctx, k8sclient, recreatedVMI.Name, namespace)

		By("Get VMI's pod")
		pod := getVMIPod(ctx, recreatedVMI)

		By("Try to evict the VMI pod; should fail, but trigger the VMI draining")
		evictNode(ctx, k8sClientSet, pod)

		By("wait for a VMI to be marked for deletion")
		waitForVMIDraining(ctx, k8sclient, vmiName, namespace)

		By("Verify the guest node was cordoned (drained) before VMI deletion")
		waitForNodeCordoned(ctx, clientSet, vmiName)

		By("remove the test finalizer")
		removeFinalizerFromVMI(ctx, k8sclient, recreatedVMI)

		By("wait for a new VMI to be created")
		getRecreatedVMI(ctx, k8sclient, vmiName, namespace, recreatedVMI.GetUID())

		By("Read the worker node from the tenant cluster, and validate its IP")
		validateNewNodeIP(ctx, k8sclient, clientSet, vmiName, namespace)
	})

	// This test will create a tenant cluster from `templates/cluster-template-ext-infra.yaml` template.
	// 1. generate secret in 'capk-system' namespace, containing external infra kubeconfig and namespace data
	// (note, the kubeconfig actually points to the same cluster, which is okay for the sake of the test)
	// 2. generate tenant cluster yaml manifests with `clusterctl generate cluster` command
	// 3. apply generated tenant cluster yaml manifests
	// 4. verify that tenant cluster machines booted and bootstrapped
	// 5. verify that tenant cluster control plane came up successfully
	It("should create a simple tenant cluster on external infrastructure", Label("externallyManaged"), func(ctx context.Context) {
		By("generating a secret with external infrastructure kubeconfig and namespace")
		kubeconfig, err := os.ReadFile(os.Getenv("KUBECONFIG"))
		Expect(err).ToNot(HaveOccurred())
		// replace api server url with default server value, so it's routable from CAPK pod
		kubeconfigStr := string(kubeconfig)
		m := regexp.MustCompile("(?m:(.*?server:).*$)")
		kubeconfigStr = m.ReplaceAllString(kubeconfigStr, "${1} https://kubernetes.default")
		externalInfraSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      externalSecretName,
				Namespace: externalSecretNamespace,
			},
			Data: map[string][]byte{
				"namespace":  []byte(namespace),
				"kubeconfig": []byte(kubeconfigStr),
			},
		}
		Expect(k8sclient.Create(ctx, externalInfraSecret)).To(Succeed())

		By("generating cluster manifests from example template")
		cmd := exec.Command(ClusterctlPath, "generate", "cluster", "kvcluster",
			"--from", "templates/cluster-template-ext-infra.yaml",
			"--kubernetes-version", os.Getenv("TENANT_CLUSTER_KUBERNETES_VERSION"),
			"--control-plane-machine-count=1",
			"--worker-machine-count=1",
			"--target-namespace", namespace)
		stdout, stderr := RunCmd(cmd)
		if len(stderr) > 0 {
			GinkgoLogr.Info("Warning", "clusterctl's stderr", string(stderr))
		}

		Expect(os.WriteFile(manifestsFile, stdout, 0644)).To(Succeed())

		By("applying cluster manifests")
		cmd = exec.Command(KubectlPath, "apply", "-f", manifestsFile)
		RunCmd(cmd)

		By("waiting for machines to be ready")
		waitForMachineReadiness(ctx, 2, 0)

		By("waiting for machines to bootstrap")
		waitForBootstrappedMachines(ctx)

		By("waiting for control plane")
		waitForControlPlane(ctx)
	})
})

func waitForVMIDraining(ctx context.Context, k8sclient client.Client, vmiName, namespace string) {
	By("wait for VMI is marked for deletion")
	Eventually(func(g Gomega) {
		vmi := getVmiByName(ctx, k8sclient, vmiName, namespace)
		g.Expect(vmi).ShouldNot(BeNil())

		vmiDebugPrintout(vmi)

		g.Expect(vmi.Status.EvacuationNodeName).ShouldNot(BeEmpty())
		g.Expect(vmi.DeletionTimestamp).ShouldNot(BeNil())
	}).WithOffset(1).
		WithTimeout(time.Minute * 5).
		WithPolling(time.Second * 5).
		Should(Succeed())
}

func waitForNodeCordoned(ctx context.Context, cl *kubernetes.Clientset, nodeName string) {
	By("wait for guest node to be cordoned")
	Eventually(func(g Gomega) {
		node, err := cl.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(node.Spec.Unschedulable).To(BeTrueBecause("expected guest node %q to be cordoned (unschedulable)", nodeName))
	}).WithOffset(1).
		WithTimeout(time.Minute * 5).
		WithPolling(time.Second * 5).
		Should(Succeed())
}

func evictNode(ctx context.Context, cli *kubernetes.Clientset, pod *corev1.Pod) {
	err := cli.CoreV1().Pods(pod.Namespace).EvictV1beta1(ctx, &policy.Eviction{
		ObjectMeta: metav1.ObjectMeta{
			Name: pod.Name,
		},
		DeleteOptions: &metav1.DeleteOptions{
			GracePeriodSeconds: ptr.To(int64(60 * 10)), // 10 minutes
		},
	})

	ExpectWithOffset(1, k8serrors.IsTooManyRequests(err)).To(BeTrue(), "should return TooManyRequests error; got %v instead", err)
}

func getRecreatedVMI(ctx context.Context, k8sclient client.Client, vmiName string, namespace string, originalUID types.UID) *kubevirtv1.VirtualMachineInstance {
	var vmi *kubevirtv1.VirtualMachineInstance

	Eventually(func(g Gomega) types.UID {
		vmi = getVmiByName(ctx, k8sclient, vmiName, namespace)
		g.Expect(vmi).ShouldNot(BeNil())

		vmiDebugPrintout(vmi)

		g.Expect(vmi.Status.EvacuationNodeName).Should(BeEmpty())
		g.Expect(vmi.DeletionTimestamp).Should(BeNil())

		return vmi.GetUID()
	}).WithOffset(1).
		WithTimeout(time.Minute * 8).
		WithPolling(time.Second * 5).
		ShouldNot(Equal(originalUID)) // make sure that a new VMI was created

	return vmi
}

func validateNewNodeIP(ctx context.Context, k8sclient client.Client, cl *kubernetes.Clientset, vmiName, namespace string) {
	Eventually(func(g Gomega) bool {
		// reading the node and the VMI again and again, because it takes time to the IPs to be synchronized
		node, err := cl.CoreV1().Nodes().Get(ctx, vmiName, metav1.GetOptions{})
		g.Expect(err).ToNot(HaveOccurred())

		var nodeIp string
		for _, address := range node.Status.Addresses {
			if address.Type == "InternalIP" {
				nodeIp = address.Address
			}
		}

		g.Expect(nodeIp).ShouldNot(BeEmpty(), "node's IP is not set")

		vmi := getVmiByName(ctx, k8sclient, vmiName, namespace)
		g.Expect(vmi).ShouldNot(BeNil())

		for _, ifs := range vmi.Status.Interfaces {
			for _, ip := range ifs.IPs {
				if ip == nodeIp {
					return true
				}
			}
		}
		return false

	}).WithTimeout(5 * time.Minute).
		WithOffset(1).
		WithPolling(10 * time.Second).
		Should(BeTrue())
}

func vmiDebugPrintout(vmi *kubevirtv1.VirtualMachineInstance) {
	GinkgoWriter.Printf(`[Debug] VMI: {"UID": "%v", "DeletionTimestamp": "%v", "EvacuationNodeName": "%s"}`+"\n",
		vmi.UID, vmi.DeletionTimestamp, vmi.Status.EvacuationNodeName)
}

func addFinalizerFromVMI(ctx context.Context, k8sclient client.Client, vmiName, namespace string) *kubevirtv1.VirtualMachineInstance {

	patchBytes := []byte(fmt.Sprintf(`[{"op": "add", "path": "/metadata/finalizers/-", "value": "%s"}]`, testFinalizer))
	patch := client.RawPatch(types.JSONPatchType, patchBytes)

	Eventually(func(g Gomega) error {
		vmi := getVmiByName(ctx, k8sclient, vmiName, namespace)
		g.Expect(vmi).ToNot(BeNil())
		return k8sclient.Patch(ctx, vmi, patch)
	}).WithOffset(1).
		WithTimeout(time.Minute).
		WithPolling(2 * time.Second).
		Should(Succeed())

	vmi := getVmiByName(ctx, k8sclient, vmiName, namespace)
	return vmi
}

func removeFinalizerFromVMI(ctx context.Context, k8sclient client.Client, vmi *kubevirtv1.VirtualMachineInstance) {
	vmi = getVmiByKey(ctx, k8sclient, client.ObjectKeyFromObject(vmi))

	index := -1
	for i, finalizer := range vmi.Finalizers {
		if finalizer == testFinalizer {
			index = i
			break
		}
	}
	ExpectWithOffset(1, index).To(BeNumerically(">=", 0))

	patchBytes := []byte(fmt.Sprintf(`[{"op": "remove", "path": "/metadata/finalizers/%d"}]`, index))
	patch := client.RawPatch(types.JSONPatchType, patchBytes)

	Eventually(func(g Gomega) error {
		return k8sclient.Patch(ctx, vmi, patch)
	}).WithOffset(1).
		WithTimeout(time.Minute).
		WithPolling(2 * time.Second).
		Should(Succeed())
}

func getVmiByName(ctx context.Context, cli client.Client, name, namespace string) *kubevirtv1.VirtualMachineInstance {
	key := client.ObjectKey{Namespace: namespace, Name: name}
	return getVmiByKey(ctx, cli, key)
}

func getVmiByKey(ctx context.Context, cli client.Client, key client.ObjectKey) *kubevirtv1.VirtualMachineInstance {
	vmi := &kubevirtv1.VirtualMachineInstance{}
	Expect(cli.Get(ctx, key, vmi)).To(Succeed())
	return vmi
}

func printObjFunc(obj any) func() string {
	return func() string {
		sb := &strings.Builder{}
		sb.WriteString("the object json is:\n")
		enc := json.NewEncoder(sb)
		enc.SetIndent("", "  ")
		err := enc.Encode(obj)
		if err != nil {
			return "got error when trying to marshal the object; " + err.Error()
		}

		sb.Write([]byte{'\n'})
		return sb.String()
	}
}
