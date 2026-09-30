/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2/textlogger"
	"k8s.io/utils/ptr"
	kubevirtv1 "kubevirt.io/api/core/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
)

const (
	capkCRDDir = "config/crd/bases"

	// cacheSyncTimeout is how long a controller waits for the informers of
	// the kinds it watches. The informer of a kind without a CRD never syncs,
	// so this is how long it takes a manager watching one to give up and stop.
	cacheSyncTimeout = 10 * time.Second
)

// TestIntegrationManagerStartup runs the manager against a real API server.
// It needs the envtest binaries and is skipped without them; run it with
// "make test-integration".
func TestIntegrationManagerStartup(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is not set; run \"make test-integration\"")
	}
	ctrl.SetLogger(textlogger.NewLogger(textlogger.NewConfig()))

	isRemediationCRD := func(path string) bool {
		return strings.Contains(filepath.Base(path), "kubevirtremediation")
	}

	// This is the upgrade path the flag protects: a new controller image on a
	// cluster whose CRDs are managed, and updated, separately.
	t.Run("without the remediation CRDs", func(t *testing.T) {
		env := startEnvtest(t, func(path string) bool { return !isRemediationCRD(path) })

		t.Run("the manager runs while remediation is disabled", func(t *testing.T) {
			enableRemediation = false
			expectWorkingManager(t, env)
		})

		t.Run("remediation cannot be enabled", func(t *testing.T) {
			g := NewWithT(t)
			enableRemediation = true
			t.Cleanup(func() { enableRemediation = false })

			err := setupReconcilers(context.Background(), newTestManager(t, env))
			g.Expect(err).To(HaveOccurred())
			g.Expect(meta.IsNoMatchError(err)).To(BeTrue(), "unexpected error: %v", err)
		})
	})

	t.Run("with the remediation CRDs", func(t *testing.T) {
		env := startEnvtest(t, func(string) bool { return true })

		t.Run("the manager runs while remediation is enabled", func(t *testing.T) {
			enableRemediation = true
			t.Cleanup(func() { enableRemediation = false })

			mgr := expectWorkingManager(t, env)

			// Not the cached client of the manager: the test reads its own writes.
			c, err := client.New(env.Config, client.Options{Scheme: mgr.GetScheme()})
			NewWithT(t).Expect(err).ToNot(HaveOccurred())
			expectRemediationToFollowClusterPause(t, c)
		})
	})
}

// startEnvtest starts an API server with the Cluster API CRDs and the CAPK
// CRDs accepted by the filter.
func startEnvtest(t *testing.T, includeCAPKCRD func(path string) bool) *envtest.Environment {
	t.Helper()
	g := NewWithT(t)

	capkCRDs, err := filepath.Glob(filepath.Join(capkCRDDir, "*.yaml"))
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(capkCRDs).ToNot(BeEmpty())

	crdPaths := []string{filepath.Join(clusterAPIModuleDir(t), "config", "crd", "bases")}
	for _, path := range capkCRDs {
		if includeCAPKCRD(path) {
			crdPaths = append(crdPaths, path)
		}
	}

	env := &envtest.Environment{
		CRDDirectoryPaths:     crdPaths,
		ErrorIfCRDPathMissing: true,
	}
	_, err = env.Start()
	g.Expect(err).ToNot(HaveOccurred())
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Logf("failed to stop envtest: %v", err)
		}
	})

	return env
}

// clusterAPIModuleDir locates the Cluster API module, which ships the CRDs of
// the Cluster and Machine kinds the CAPK controllers watch.
func clusterAPIModuleDir(t *testing.T) string {
	t.Helper()

	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "sigs.k8s.io/cluster-api").Output()
	if err != nil {
		var stderr []byte
		if exitErr := (&exec.ExitError{}); errors.As(err, &exitErr) {
			stderr = exitErr.Stderr
		}
		t.Fatalf("failed to locate the sigs.k8s.io/cluster-api module: %v: %s", err, stderr)
	}

	return strings.TrimSpace(string(out))
}

func newTestManager(t *testing.T, env *envtest.Environment) ctrl.Manager {
	t.Helper()
	g := NewWithT(t)

	myscheme, err := registerScheme()
	g.Expect(err).ToNot(HaveOccurred())

	mgr, err := ctrl.NewManager(env.Config, ctrl.Options{
		Scheme:                 myscheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Controller: config.Controller{
			CacheSyncTimeout: cacheSyncTimeout,
			// Every manager of this test process registers the same controllers.
			SkipNameValidation: ptr.To(true),
		},
	})
	g.Expect(err).ToNot(HaveOccurred())

	return mgr
}

// expectWorkingManager sets up the reconcilers the way main does, and expects
// the manager to keep running and its controllers to reconcile.
func expectWorkingManager(t *testing.T, env *envtest.Environment) ctrl.Manager {
	t.Helper()
	g := NewWithT(t)

	ctx, cancel := context.WithCancel(context.Background())
	mgr := newTestManager(t, env)
	g.Expect(setupReconcilers(ctx, mgr)).To(Succeed())

	stopped := make(chan error, 1)
	go func() { stopped <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(30 * time.Second):
			t.Log("the manager did not stop in time")
		}
	})

	// The KubevirtMachineTemplate controller publishes the capacity of the
	// VM template; seeing it means the controllers were started.
	template := &infrav1.KubevirtMachineTemplate{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "startup-", Namespace: metav1.NamespaceDefault},
		Spec: infrav1.KubevirtMachineTemplateSpec{
			Template: infrav1.KubevirtMachineTemplateResource{
				Spec: infrav1.KubevirtMachineSpec{
					VirtualMachineTemplate: infrav1.VirtualMachineTemplateSpec{
						Spec: kubevirtv1.VirtualMachineSpec{
							Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
								Spec: kubevirtv1.VirtualMachineInstanceSpec{
									Domain: kubevirtv1.DomainSpec{CPU: &kubevirtv1.CPU{Cores: 2}},
								},
							},
						},
					},
				},
			},
		},
	}
	g.Expect(mgr.GetClient().Create(ctx, template)).To(Succeed())
	t.Cleanup(func() {
		if err := mgr.GetClient().Delete(context.Background(), template); err != nil {
			t.Logf("failed to delete %s: %v", template.Name, err)
		}
	})

	g.Eventually(func(g Gomega) {
		g.Expect(mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(template), template)).To(Succeed())
		g.Expect(template.Status.Capacity).To(HaveKey(corev1.ResourceCPU))
	}).WithTimeout(time.Minute).WithPolling(time.Second).Should(Succeed())

	// A manager whose informers cannot sync stops once the cache sync times
	// out; outlast that.
	g.Consistently(stopped).WithTimeout(cacheSyncTimeout+5*time.Second).WithPolling(time.Second).
		ShouldNot(Receive(), "the manager stopped")

	return mgr
}

// expectRemediationToFollowClusterPause verifies the watches of the
// KubevirtRemediation controller: a request is left alone while its Cluster is
// paused, and picked up as soon as the Cluster is unpaused. Without a
// KubevirtMachine behind the Machine there is nothing to restart, so picking
// it up means handing the Machine back.
func expectRemediationToFollowClusterPause(t *testing.T, c client.Client) {
	t.Helper()
	g := NewWithT(t)
	ctx := context.Background()

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "paused", Namespace: metav1.NamespaceDefault},
		Spec:       clusterv1.ClusterSpec{Paused: ptr.To(true)},
	}
	g.Expect(c.Create(ctx, cluster)).To(Succeed())

	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "paused-worker",
			Namespace: cluster.Namespace,
			Labels:    map[string]string{clusterv1.ClusterNameLabel: cluster.Name},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: cluster.Name,
			Bootstrap:   clusterv1.Bootstrap{DataSecretName: ptr.To("bootstrap")},
			InfrastructureRef: clusterv1.ContractVersionedObjectReference{
				APIGroup: infrav1.GroupVersion.Group,
				Kind:     "KubevirtMachine",
				Name:     "paused-worker",
			},
		},
	}
	g.Expect(c.Create(ctx, machine)).To(Succeed())

	// What the MachineHealthCheck controller creates: named after the
	// Machine, owned by it, labelled with the cluster name.
	remediation := &infrav1.KubevirtRemediation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      machine.Name,
			Namespace: machine.Namespace,
			Labels:    map[string]string{clusterv1.ClusterNameLabel: cluster.Name},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: clusterv1.GroupVersion.String(),
				Kind:       "Machine",
				Name:       machine.Name,
				UID:        machine.UID,
			}},
		},
	}
	g.Expect(c.Create(ctx, remediation)).To(Succeed())

	phase := func(g Gomega) string {
		g.Expect(c.Get(ctx, client.ObjectKeyFromObject(remediation), remediation)).To(Succeed())
		return remediation.Status.Phase
	}
	g.Consistently(phase).WithTimeout(5*time.Second).WithPolling(time.Second).Should(BeEmpty(),
		"the remediation was processed although the Cluster is paused")

	patch := client.MergeFrom(cluster.DeepCopy())
	cluster.Spec.Paused = ptr.To(false)
	g.Expect(c.Patch(ctx, cluster, patch)).To(Succeed())

	g.Eventually(phase).WithTimeout(time.Minute).WithPolling(time.Second).Should(Equal(infrav1.PhaseFailed),
		"the remediation was not picked up after the Cluster was unpaused")
}
