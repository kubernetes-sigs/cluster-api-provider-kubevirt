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

package controllers

import (
	gocontext "context"
	"time"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	kubevirtv1 "kubevirt.io/api/core/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/conditions"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	infraclustermock "sigs.k8s.io/cluster-api-provider-kubevirt/pkg/infracluster/mock"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/testing"
)

var _ = Describe("KubevirtRemediation controller", func() {
	const (
		namespace   = "test-ns"
		machineName = "test-machine"
		clusterName = "test-cluster"
	)

	var (
		mockCtrl         *gomock.Controller
		infraClusterMock *infraclustermock.MockInfraCluster
		fakeClient       ctrlclient.Client
		interceptors     interceptor.Funcs
		reconciler       *KubevirtRemediationReconciler

		cluster         *clusterv1.Cluster
		machine         *clusterv1.Machine
		kubevirtMachine *infrav1.KubevirtMachine
		vm              *kubevirtv1.VirtualMachine
		vmi             *kubevirtv1.VirtualMachineInstance
		remediation     *infrav1.KubevirtRemediation
	)

	runStrategy := func(rs kubevirtv1.VirtualMachineRunStrategy) *kubevirtv1.VirtualMachineRunStrategy {
		return &rs
	}

	BeforeEach(func() {
		interceptors = interceptor.Funcs{}
		mockCtrl = gomock.NewController(GinkgoT())
		infraClusterMock = infraclustermock.NewMockInfraCluster(mockCtrl)

		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      clusterName,
				Namespace: namespace,
			},
		}

		machine = &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      machineName,
				Namespace: namespace,
				UID:       "machine-uid",
			},
			Spec: clusterv1.MachineSpec{
				ClusterName: clusterName,
				InfrastructureRef: clusterv1.ContractVersionedObjectReference{
					Kind:     "KubevirtMachine",
					Name:     machineName,
					APIGroup: infrav1.GroupVersion.Group,
				},
			},
		}

		kubevirtMachine = &infrav1.KubevirtMachine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      machineName,
				Namespace: namespace,
			},
		}

		vm = &kubevirtv1.VirtualMachine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      machineName,
				Namespace: namespace,
			},
			Spec: kubevirtv1.VirtualMachineSpec{
				RunStrategy: runStrategy(kubevirtv1.RunStrategyAlways),
			},
		}

		vmi = &kubevirtv1.VirtualMachineInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      machineName,
				Namespace: namespace,
			},
		}

		remediation = &infrav1.KubevirtRemediation{
			ObjectMeta: metav1.ObjectMeta{
				Name:      machineName,
				Namespace: namespace,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: clusterv1.GroupVersion.String(),
						Kind:       "Machine",
						Name:       machineName,
						UID:        "machine-uid",
					},
				},
			},
			Spec: infrav1.KubevirtRemediationSpec{
				Strategy: &infrav1.RemediationStrategy{
					Type:           infrav1.RebootRemediationType,
					RetryLimit:     2,
					TimeoutSeconds: 300,
				},
			},
		}
	})

	AfterEach(func() {
		mockCtrl.Finish()
	})

	setup := func(objects ...ctrlclient.Object) {
		fakeClient = ctrlfake.NewClientBuilder().
			WithScheme(testing.SetupScheme()).
			WithObjects(objects...).
			WithStatusSubresource(&infrav1.KubevirtRemediation{}, &clusterv1.Machine{}).
			WithInterceptorFuncs(interceptors).
			Build()
		reconciler = &KubevirtRemediationReconciler{
			Client:       fakeClient,
			InfraCluster: infraClusterMock,
		}
	}

	reconcile := func() (ctrl.Result, error) {
		return reconciler.Reconcile(gocontext.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: namespace, Name: machineName},
		})
	}

	getRemediation := func() *infrav1.KubevirtRemediation {
		out := &infrav1.KubevirtRemediation{}
		Expect(fakeClient.Get(gocontext.Background(),
			types.NamespacedName{Namespace: namespace, Name: machineName}, out)).To(Succeed())
		return out
	}

	expectVMIUntouched := func() {
		Expect(fakeClient.Get(gocontext.Background(),
			types.NamespacedName{Namespace: namespace, Name: machineName}, &kubevirtv1.VirtualMachineInstance{})).To(Succeed())
	}

	expectVMIDeleted := func() {
		err := fakeClient.Get(gocontext.Background(),
			types.NamespacedName{Namespace: namespace, Name: machineName}, &kubevirtv1.VirtualMachineInstance{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	}

	expiredBootWindow := func(retryCount int32) infrav1.KubevirtRemediationStatus {
		// Well beyond every timeout the tests use, the 600s default included.
		lastRemediated := metav1.NewTime(time.Now().Add(-time.Hour))
		return infrav1.KubevirtRemediationStatus{
			Phase:          infrav1.PhaseWaiting,
			RetryCount:     retryCount,
			LastRemediated: &lastRemediated,
		}
	}

	setHealthCheckSucceeded := func(status metav1.ConditionStatus) {
		conditions.Set(machine, metav1.Condition{
			Type:   clusterv1.MachineHealthCheckSucceededCondition,
			Status: status,
			Reason: "Test",
		})
	}

	getMachine := func() *clusterv1.Machine {
		out := &clusterv1.Machine{}
		Expect(fakeClient.Get(gocontext.Background(),
			types.NamespacedName{Namespace: namespace, Name: machineName}, out)).To(Succeed())
		return out
	}

	expectInfraClient := func() *gomock.Call {
		return infraClusterMock.EXPECT().
			GenerateInfraClusterClient(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(fakeClient, namespace, nil)
	}

	It("restarts the VMI on a fresh remediation and moves to Waiting", func() {
		setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
		expectInfraClient()

		result, err := reconcile()
		Expect(err).ShouldNot(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(300 * time.Second))

		// The VMI must be gone; the KubeVirt VM controller recreates it.
		err = fakeClient.Get(gocontext.Background(),
			types.NamespacedName{Namespace: namespace, Name: machineName}, &kubevirtv1.VirtualMachineInstance{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		updated := getRemediation()
		Expect(updated.Status.Phase).To(Equal(infrav1.PhaseWaiting))
		Expect(updated.Status.RetryCount).To(Equal(int32(1)))
		Expect(updated.Status.LastRemediated).ToNot(BeNil())
	})

	It("tolerates a VMI that is already gone", func() {
		setup(remediation, cluster, machine, kubevirtMachine, vm) // no VMI
		expectInfraClient()

		_, err := reconcile()
		Expect(err).ShouldNot(HaveOccurred())
		Expect(getRemediation().Status.Phase).To(Equal(infrav1.PhaseWaiting))
	})

	It("waits without restarting while inside the boot window", func() {
		lastRemediated := metav1.NewTime(time.Now().Add(-1 * time.Minute))
		remediation.Status = infrav1.KubevirtRemediationStatus{
			Phase:          infrav1.PhaseWaiting,
			RetryCount:     1,
			LastRemediated: &lastRemediated,
		}
		setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
		// No infra client expected: nothing is restarted.

		result, err := reconcile()
		Expect(err).ShouldNot(HaveOccurred())
		Expect(result.RequeueAfter).To(BeNumerically(">", 0))
		Expect(result.RequeueAfter).To(BeNumerically("<=", 300*time.Second))

		// The VMI is untouched.
		Expect(fakeClient.Get(gocontext.Background(),
			types.NamespacedName{Namespace: namespace, Name: machineName}, &kubevirtv1.VirtualMachineInstance{})).To(Succeed())
		Expect(getRemediation().Status.RetryCount).To(Equal(int32(1)))
	})

	It("restarts again when the boot window expired and retries remain", func() {
		lastRemediated := metav1.NewTime(time.Now().Add(-10 * time.Minute))
		remediation.Status = infrav1.KubevirtRemediationStatus{
			Phase:          infrav1.PhaseWaiting,
			RetryCount:     1,
			LastRemediated: &lastRemediated,
		}
		setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
		expectInfraClient()

		_, err := reconcile()
		Expect(err).ShouldNot(HaveOccurred())

		updated := getRemediation()
		Expect(updated.Status.Phase).To(Equal(infrav1.PhaseWaiting))
		Expect(updated.Status.RetryCount).To(Equal(int32(2)))
	})

	It("hands the Machine back to CAPI when the retry budget is exhausted", func() {
		lastRemediated := metav1.NewTime(time.Now().Add(-10 * time.Minute))
		remediation.Status = infrav1.KubevirtRemediationStatus{
			Phase:          infrav1.PhaseWaiting,
			RetryCount:     2, // == RetryLimit
			LastRemediated: &lastRemediated,
		}
		setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)

		_, err := reconcile()
		Expect(err).ShouldNot(HaveOccurred())

		Expect(getRemediation().Status.Phase).To(Equal(infrav1.PhaseFailed))

		cond := conditions.Get(getMachine(), clusterv1.MachineOwnerRemediatedCondition)
		Expect(cond).ToNot(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(clusterv1.MachineOwnerRemediatedWaitingForRemediationReason))
	})

	It("hands the Machine back to CAPI when the VM run strategy cannot restart", func() {
		vm.Spec.RunStrategy = runStrategy(kubevirtv1.RunStrategyHalted)
		setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
		expectInfraClient()

		_, err := reconcile()
		Expect(err).ShouldNot(HaveOccurred())

		Expect(getRemediation().Status.Phase).To(Equal(infrav1.PhaseFailed))

		cond := conditions.Get(getMachine(), clusterv1.MachineOwnerRemediatedCondition)
		Expect(cond).ToNot(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))

		// The VMI must not have been deleted: with runStrategy Halted nothing
		// would bring it back.
		Expect(fakeClient.Get(gocontext.Background(),
			types.NamespacedName{Namespace: namespace, Name: machineName}, &kubevirtv1.VirtualMachineInstance{})).To(Succeed())
	})

	DescribeTable("hands the Machine back to CAPI when a volume does not persist across a restart",
		func(source kubevirtv1.VolumeSource) {
			vm.Spec.Template = &kubevirtv1.VirtualMachineInstanceTemplateSpec{
				Spec: kubevirtv1.VirtualMachineInstanceSpec{
					Volumes: []kubevirtv1.Volume{
						{Name: "cloudinit", VolumeSource: kubevirtv1.VolumeSource{
							CloudInitNoCloud: &kubevirtv1.CloudInitNoCloudSource{},
						}},
						{Name: "root", VolumeSource: source},
					},
				},
			}
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
			expectInfraClient()

			_, err := reconcile()
			Expect(err).ShouldNot(HaveOccurred())

			Expect(getRemediation().Status.Phase).To(Equal(infrav1.PhaseFailed))

			cond := conditions.Get(getMachine(), clusterv1.MachineOwnerRemediatedCondition)
			Expect(cond).ToNot(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Message).To(ContainSubstring(`volume "root"`))

			// Deleting the VMI would wipe the node's state for nothing.
			expectVMIUntouched()
		},
		Entry("containerDisk", kubevirtv1.VolumeSource{
			ContainerDisk: &kubevirtv1.ContainerDiskSource{Image: "example.com/node:latest"},
		}),
		Entry("ephemeral", kubevirtv1.VolumeSource{
			Ephemeral: &kubevirtv1.EphemeralVolumeSource{},
		}),
	)

	It("restarts a VM whose disks are all persistent", func() {
		vm.Spec.Template = &kubevirtv1.VirtualMachineInstanceTemplateSpec{
			Spec: kubevirtv1.VirtualMachineInstanceSpec{
				Volumes: []kubevirtv1.Volume{
					{Name: "root", VolumeSource: kubevirtv1.VolumeSource{
						DataVolume: &kubevirtv1.DataVolumeSource{Name: "root-dv"},
					}},
					{Name: "cloudinit", VolumeSource: kubevirtv1.VolumeSource{
						CloudInitNoCloud: &kubevirtv1.CloudInitNoCloudSource{},
					}},
				},
			},
		}
		setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
		expectInfraClient()

		_, err := reconcile()
		Expect(err).ShouldNot(HaveOccurred())
		Expect(getRemediation().Status.Phase).To(Equal(infrav1.PhaseWaiting))
		expectVMIDeleted()
	})

	Context("when the restart fails", func() {
		It("retries instead of replacing the Machine on a transient error", func() {
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
			infraClusterMock.EXPECT().
				GenerateInfraClusterClient(gomock.Any(), gomock.Any(), gomock.Any()).
				Return(nil, "", errors.New("infra cluster unreachable"))

			_, err := reconcile()
			Expect(err).Should(HaveOccurred())

			updated := getRemediation()
			Expect(updated.Status.Phase).To(Equal(infrav1.PhaseRunning))
			Expect(updated.Status.RetryCount).To(BeZero())
			Expect(conditions.Get(getMachine(), clusterv1.MachineOwnerRemediatedCondition)).To(BeNil())
			expectVMIUntouched()
		})

		It("hands the Machine back to CAPI when the VirtualMachine is gone", func() {
			setup(remediation, cluster, machine, kubevirtMachine) // no VM
			expectInfraClient()

			_, err := reconcile()
			Expect(err).ShouldNot(HaveOccurred())

			Expect(getRemediation().Status.Phase).To(Equal(infrav1.PhaseFailed))
			cond := conditions.Get(getMachine(), clusterv1.MachineOwnerRemediatedCondition)
			Expect(cond).ToNot(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		})

		It("hands the Machine back to CAPI when the KubevirtMachine is gone", func() {
			setup(remediation, cluster, machine, vm, vmi) // no KubevirtMachine
			// No infra client expected: the lookup fails before it is needed.

			_, err := reconcile()
			Expect(err).ShouldNot(HaveOccurred())

			Expect(getRemediation().Status.Phase).To(Equal(infrav1.PhaseFailed))
			cond := conditions.Get(getMachine(), clusterv1.MachineOwnerRemediatedCondition)
			Expect(cond).ToNot(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			expectVMIUntouched()
		})

		DescribeTable("retries when the infra cluster fails a request",
			func(funcs interceptor.Funcs) {
				interceptors = funcs
				setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
				expectInfraClient()

				_, err := reconcile()
				Expect(err).Should(HaveOccurred())

				Expect(getRemediation().Status.Phase).To(Equal(infrav1.PhaseRunning))
				Expect(getRemediation().Status.RetryCount).To(BeZero())
				Expect(conditions.Get(getMachine(), clusterv1.MachineOwnerRemediatedCondition)).To(BeNil())
			},
			Entry("getting the VirtualMachine", interceptor.Funcs{
				Get: func(ctx gocontext.Context, c ctrlclient.WithWatch, key ctrlclient.ObjectKey, obj ctrlclient.Object, opts ...ctrlclient.GetOption) error {
					if _, ok := obj.(*kubevirtv1.VirtualMachine); ok {
						return errors.New("infra cluster timed out")
					}
					return c.Get(ctx, key, obj, opts...)
				},
			}),
			Entry("deleting the VirtualMachineInstance", interceptor.Funcs{
				Delete: func(ctx gocontext.Context, c ctrlclient.WithWatch, obj ctrlclient.Object, opts ...ctrlclient.DeleteOption) error {
					if _, ok := obj.(*kubevirtv1.VirtualMachineInstance); ok {
						return errors.New("infra cluster timed out")
					}
					return c.Delete(ctx, obj, opts...)
				},
			}),
		)

		It("restarts on the next attempt after a transient error, without losing count", func() {
			remediation.Status = expiredBootWindow(1)
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
			gomock.InOrder(
				infraClusterMock.EXPECT().
					GenerateInfraClusterClient(gomock.Any(), gomock.Any(), gomock.Any()).
					Return(nil, "", errors.New("infra cluster unreachable")),
				infraClusterMock.EXPECT().
					GenerateInfraClusterClient(gomock.Any(), gomock.Any(), gomock.Any()).
					Return(fakeClient, namespace, nil),
			)

			_, err := reconcile()
			Expect(err).Should(HaveOccurred())
			Expect(getRemediation().Status.Phase).To(Equal(infrav1.PhaseRunning))
			Expect(getRemediation().Status.RetryCount).To(Equal(int32(1)))

			_, err = reconcile()
			Expect(err).ShouldNot(HaveOccurred())
			expectVMIDeleted()
			Expect(getRemediation().Status.Phase).To(Equal(infrav1.PhaseWaiting))
			Expect(getRemediation().Status.RetryCount).To(Equal(int32(2)))
		})

		It("keeps the remediation open when the Machine cannot be patched", func() {
			remediation.Status = expiredBootWindow(2) // == RetryLimit
			interceptors = interceptor.Funcs{
				SubResourcePatch: func(ctx gocontext.Context, c ctrlclient.Client, subResource string, obj ctrlclient.Object, patch ctrlclient.Patch, opts ...ctrlclient.SubResourcePatchOption) error {
					if _, ok := obj.(*clusterv1.Machine); ok {
						return errors.New("conflict")
					}
					return c.SubResource(subResource).Patch(ctx, obj, patch, opts...)
				},
			}
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)

			_, err := reconcile()
			Expect(err).Should(HaveOccurred())
			Expect(getRemediation().Status.Phase).To(Equal(infrav1.PhaseWaiting))
		})

		It("does not hand back a Machine that still passes its health check", func() {
			// A brand-new request can be seen before the MachineHealthCheck
			// patched the Machine; its owner would drop the condition.
			vm.Spec.RunStrategy = runStrategy(kubevirtv1.RunStrategyHalted)
			setHealthCheckSucceeded(metav1.ConditionTrue)
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
			expectInfraClient().Times(2)

			_, err := reconcile()
			Expect(err).ShouldNot(HaveOccurred())
			Expect(getRemediation().Status.Phase).To(Equal(infrav1.PhaseRunning))
			Expect(conditions.Get(getMachine(), clusterv1.MachineOwnerRemediatedCondition)).To(BeNil())

			unhealthy := getMachine()
			conditions.Set(unhealthy, metav1.Condition{
				Type:   clusterv1.MachineHealthCheckSucceededCondition,
				Status: metav1.ConditionFalse,
				Reason: "Test",
			})
			Expect(fakeClient.Status().Update(gocontext.Background(), unhealthy)).To(Succeed())

			_, err = reconcile()
			Expect(err).ShouldNot(HaveOccurred())
			Expect(getRemediation().Status.Phase).To(Equal(infrav1.PhaseFailed))
			expectVMIUntouched()
		})
	})

	It("hands the Machine back to CAPI for a strategy it does not implement", func() {
		remediation.Spec.Strategy.Type = "Reprovision"
		setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)

		_, err := reconcile()
		Expect(err).ShouldNot(HaveOccurred())
		Expect(getRemediation().Status.Phase).To(Equal(infrav1.PhaseFailed))
		cond := conditions.Get(getMachine(), clusterv1.MachineOwnerRemediatedCondition)
		Expect(cond).ToNot(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		expectVMIUntouched()
	})

	It("applies the API defaults when no strategy is set", func() {
		remediation.Spec.Strategy = nil
		setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
		expectInfraClient()

		result, err := reconcile()
		Expect(err).ShouldNot(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(600 * time.Second))

		// retryLimit defaults to 1: the restart above was the only one.
		expired := getRemediation()
		expired.Status = expiredBootWindow(1)
		Expect(fakeClient.Status().Update(gocontext.Background(), expired)).To(Succeed())

		_, err = reconcile()
		Expect(err).ShouldNot(HaveOccurred())
		Expect(getRemediation().Status.Phase).To(Equal(infrav1.PhaseFailed))
	})

	It("treats a Waiting remediation without a timestamp as expired", func() {
		remediation.Status = infrav1.KubevirtRemediationStatus{Phase: infrav1.PhaseWaiting, RetryCount: 1}
		setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
		expectInfraClient()

		_, err := reconcile()
		Expect(err).ShouldNot(HaveOccurred())
		Expect(getRemediation().Status.RetryCount).To(Equal(int32(2)))
	})

	It("does nothing when the owner Machine is already gone", func() {
		setup(remediation, cluster, kubevirtMachine, vm, vmi) // no Machine

		result, err := reconcile()
		Expect(err).ShouldNot(HaveOccurred())
		Expect(result.RequeueAfter).To(BeZero())
		expectVMIUntouched()
	})

	It("returns an error when the Cluster cannot be found", func() {
		setup(remediation, machine, kubevirtMachine, vm, vmi) // no Cluster

		_, err := reconcile()
		Expect(err).Should(HaveOccurred())
		expectVMIUntouched()
	})

	Context("right after the Machine was unpaused", func() {
		pausedCondition := func(status metav1.ConditionStatus, since time.Duration) metav1.Condition {
			return metav1.Condition{
				Type:               clusterv1.PausedCondition,
				Status:             status,
				Reason:             "Test",
				LastTransitionTime: metav1.NewTime(time.Now().Add(-since)),
			}
		}
		setPaused := func(status metav1.ConditionStatus, since time.Duration) {
			conditions.Set(machine, pausedCondition(status, since))
		}

		BeforeEach(func() {
			// Exhausted budget and expired window: without the grace period
			// this escalates straight to replacement on a stale verdict.
			remediation.Status = expiredBootWindow(2)
			setHealthCheckSucceeded(metav1.ConditionFalse)
		})

		expectIdle := func() ctrl.Result {
			result, err := reconcile()
			Expect(err).ShouldNot(HaveOccurred())
			Expect(getRemediation().Status.Phase).To(Equal(infrav1.PhaseWaiting))
			Expect(conditions.Get(getMachine(), clusterv1.MachineOwnerRemediatedCondition)).To(BeNil())
			expectVMIUntouched()
			return result
		}

		It("waits while the Machine controller still reports the Machine as paused", func() {
			setPaused(metav1.ConditionTrue, time.Hour)
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)

			Expect(expectIdle().RequeueAfter).To(Equal(resumeGracePeriod))
		})

		It("waits out the rest of the grace period", func() {
			setPaused(metav1.ConditionFalse, 10*time.Second)
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)

			result := expectIdle()
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))
			Expect(result.RequeueAfter).To(BeNumerically("<=", resumeGracePeriod-10*time.Second))
		})

		// Machines that no MachineSet owns learn about a Cluster pause late;
		// the Cluster's own condition is what covers them.
		It("waits while the Cluster controller still reports the Cluster as paused", func() {
			setPaused(metav1.ConditionFalse, time.Hour)
			conditions.Set(cluster, pausedCondition(metav1.ConditionTrue, time.Hour))
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)

			Expect(expectIdle().RequeueAfter).To(Equal(resumeGracePeriod))
		})

		It("waits out the rest of the grace period of the Cluster", func() {
			setPaused(metav1.ConditionFalse, time.Hour)
			conditions.Set(cluster, pausedCondition(metav1.ConditionFalse, 10*time.Second))
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)

			result := expectIdle()
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))
			Expect(result.RequeueAfter).To(BeNumerically("<=", resumeGracePeriod-10*time.Second))
		})

		It("ignores the Paused condition of a Cluster that carries the paused annotation", func() {
			// The annotation keeps the condition True for as long as it is
			// there, but pauses neither the Machines nor the health checks.
			cluster.Annotations = map[string]string{clusterv1.PausedAnnotation: ""}
			conditions.Set(cluster, pausedCondition(metav1.ConditionTrue, time.Hour))
			setPaused(metav1.ConditionFalse, time.Hour)
			remediation.Status = expiredBootWindow(1)
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
			expectInfraClient()

			_, err := reconcile()
			Expect(err).ShouldNot(HaveOccurred())
			expectVMIDeleted()
			Expect(getRemediation().Status.RetryCount).To(Equal(int32(2)))
		})

		It("holds back the first restart as well", func() {
			remediation.Status = infrav1.KubevirtRemediationStatus{}
			setPaused(metav1.ConditionFalse, 10*time.Second)
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
			// No infra client expected: nothing is restarted.

			result, err := reconcile()
			Expect(err).ShouldNot(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))
			Expect(getRemediation().Status.Phase).To(BeEmpty())
			expectVMIUntouched()
		})

		It("acts again once the grace period is over", func() {
			conditions.Set(cluster, pausedCondition(metav1.ConditionFalse, 2*resumeGracePeriod))
			setPaused(metav1.ConditionFalse, 2*resumeGracePeriod)
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)

			_, err := reconcile()
			Expect(err).ShouldNot(HaveOccurred())
			Expect(getRemediation().Status.Phase).To(Equal(infrav1.PhaseFailed))
		})
	})

	Context("when paused", func() {
		// An expired boot window with retries left: unpaused, this restarts.
		BeforeEach(func() {
			lastRemediated := metav1.NewTime(time.Now().Add(-10 * time.Minute))
			remediation.Status = infrav1.KubevirtRemediationStatus{
				Phase:          infrav1.PhaseWaiting,
				RetryCount:     1,
				LastRemediated: &lastRemediated,
			}
		})

		expectNoAction := func() {
			// No infra client expected: nothing is restarted.
			result, err := reconcile()
			Expect(err).ShouldNot(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			expectVMIUntouched()
			updated := getRemediation()
			Expect(updated.Status.Phase).To(Equal(infrav1.PhaseWaiting))
			Expect(updated.Status.RetryCount).To(Equal(int32(1)))
			Expect(conditions.Get(getMachine(), clusterv1.MachineOwnerRemediatedCondition)).To(BeNil())
		}

		It("does not restart while the Cluster is paused", func() {
			cluster.Spec.Paused = ptr.To(true)
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
			expectNoAction()
		})

		It("does not restart while the Machine has the paused annotation", func() {
			machine.Annotations = map[string]string{clusterv1.PausedAnnotation: ""}
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
			expectNoAction()
		})

		It("does not restart while the KubevirtRemediation has the paused annotation", func() {
			remediation.Annotations = map[string]string{clusterv1.PausedAnnotation: ""}
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
			expectNoAction()
		})

		It("does not escalate an exhausted remediation while the Cluster is paused", func() {
			remediation.Status.RetryCount = 2 // == RetryLimit
			cluster.Spec.Paused = ptr.To(true)
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)

			_, err := reconcile()
			Expect(err).ShouldNot(HaveOccurred())
			Expect(getRemediation().Status.Phase).To(Equal(infrav1.PhaseWaiting))
			Expect(conditions.Get(getMachine(), clusterv1.MachineOwnerRemediatedCondition)).To(BeNil())
		})

		It("resumes once the Cluster is unpaused", func() {
			cluster.Spec.Paused = ptr.To(true)
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
			expectNoAction()

			unpaused := &clusterv1.Cluster{}
			Expect(fakeClient.Get(gocontext.Background(),
				types.NamespacedName{Namespace: namespace, Name: clusterName}, unpaused)).To(Succeed())
			unpaused.Spec.Paused = ptr.To(false)
			Expect(fakeClient.Update(gocontext.Background(), unpaused)).To(Succeed())

			expectInfraClient()
			_, err := reconcile()
			Expect(err).ShouldNot(HaveOccurred())
			Expect(getRemediation().Status.RetryCount).To(Equal(int32(2)))
		})
	})

	Context("with the Machine's HealthCheckSucceeded condition", func() {
		It("does not restart again once the Machine passes its health check", func() {
			remediation.Status = expiredBootWindow(1)
			setHealthCheckSucceeded(metav1.ConditionTrue)
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
			// No infra client expected: nothing is restarted.

			result, err := reconcile()
			Expect(err).ShouldNot(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			expectVMIUntouched()
			Expect(getRemediation().Status.RetryCount).To(Equal(int32(1)))
		})

		It("does not replace a Machine that passes its health check", func() {
			remediation.Status = expiredBootWindow(2) // == RetryLimit
			setHealthCheckSucceeded(metav1.ConditionTrue)
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)

			_, err := reconcile()
			Expect(err).ShouldNot(HaveOccurred())

			Expect(getRemediation().Status.Phase).To(Equal(infrav1.PhaseWaiting))
			Expect(conditions.Get(getMachine(), clusterv1.MachineOwnerRemediatedCondition)).To(BeNil())
		})

		It("restarts again while the Machine keeps failing its health check", func() {
			remediation.Status = expiredBootWindow(1)
			setHealthCheckSucceeded(metav1.ConditionFalse)
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
			expectInfraClient()

			_, err := reconcile()
			Expect(err).ShouldNot(HaveOccurred())

			expectVMIDeleted()
			Expect(getRemediation().Status.RetryCount).To(Equal(int32(2)))
		})

		It("does not retry a restart that failed transiently once the Machine passes its health check", func() {
			// What a transient error leaves behind: Running, with the count
			// of the restarts that did happen.
			remediation.Status = expiredBootWindow(1)
			remediation.Status.Phase = infrav1.PhaseRunning
			setHealthCheckSucceeded(metav1.ConditionTrue)
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
			// No infra client expected: nothing is restarted.

			_, err := reconcile()
			Expect(err).ShouldNot(HaveOccurred())
			expectVMIUntouched()
			Expect(getRemediation().Status.RetryCount).To(Equal(int32(1)))
		})

		It("still issues the first restart when the condition is True", func() {
			// The MachineHealthCheck creates the request before it patches the
			// condition, and other controllers may request a restart for a
			// fault that a Ready-based health check does not see.
			setHealthCheckSucceeded(metav1.ConditionTrue)
			setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)
			expectInfraClient()

			_, err := reconcile()
			Expect(err).ShouldNot(HaveOccurred())

			expectVMIDeleted()
			Expect(getRemediation().Status.RetryCount).To(Equal(int32(1)))
		})
	})

	It("maps a Machine to the remediation request named after it", func() {
		Expect(machineToRemediation(gocontext.Background(), machine)).To(ConsistOf(ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: namespace, Name: machineName},
		}))
	})

	It("does nothing once the remediation is Failed", func() {
		remediation.Status.Phase = infrav1.PhaseFailed
		setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)

		result, err := reconcile()
		Expect(err).ShouldNot(HaveOccurred())
		Expect(result.RequeueAfter).To(BeZero())
	})

	It("does nothing while the Machine is being deleted", func() {
		now := metav1.Now()
		machine.DeletionTimestamp = &now
		machine.Finalizers = []string{"test.finalizer.cluster.x-k8s.io"}
		setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)

		result, err := reconcile()
		Expect(err).ShouldNot(HaveOccurred())
		Expect(result.RequeueAfter).To(BeZero())

		// The VMI is untouched.
		Expect(fakeClient.Get(gocontext.Background(),
			types.NamespacedName{Namespace: namespace, Name: machineName}, &kubevirtv1.VirtualMachineInstance{})).To(Succeed())
	})

	It("ignores a remediation without an owner Machine", func() {
		remediation.OwnerReferences = nil
		setup(remediation, cluster, machine, kubevirtMachine, vm, vmi)

		result, err := reconcile()
		Expect(err).ShouldNot(HaveOccurred())
		Expect(result.RequeueAfter).To(BeZero())
	})
})
