package controllers

import (
	gocontext "context"
	"errors"
	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	kubevirtv1 "kubevirt.io/api/core/v1"
	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/workloadcluster/mock"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

var _ = Describe("Test VMI Controller", func() {

	const (
		clusterName         = "test"
		clusterNamespace    = clusterName + "-cluster"
		clusterInstanceName = clusterName + "-1234"
		nodeName            = "worker-node-1"
	)

	Context("Test VmiEviction reconciler", func() {
		var (
			mockCtrl   *gomock.Controller
			fakeClient client.Client
			vmi        *kubevirtv1.VirtualMachineInstance
			cluster    *clusterv1.Cluster
			wlCluster  *mock.MockWorkloadCluster
		)

		BeforeEach(func() {
			mockCtrl = gomock.NewController(GinkgoT())

			vmi = &kubevirtv1.VirtualMachineInstance{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "test-cluster",
					Name:      nodeName,
					Labels: map[string]string{
						infrav1.KubevirtMachineNamespaceLabel: clusterNamespace,
						clusterv1.ClusterLabelName:            clusterInstanceName,
					},
				},
			}

			cluster = &clusterv1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: clusterNamespace,
					Name:      clusterInstanceName,
				},
				Spec: clusterv1.ClusterSpec{
					InfrastructureRef: &corev1.ObjectReference{
						Kind:      "Secret",
						Namespace: clusterNamespace,
						Name:      clusterInstanceName,
					},
				},
			}

			wlCluster = mock.NewMockWorkloadCluster(mockCtrl)
		})

		It("Should ignore vmi if it already deleted", func() {
			fakeClient = fake.NewClientBuilder().WithScheme(setupScheme()).Build()

			// make sure we never get into darin process, but exit earlier
			wlCluster.EXPECT().GenerateWorkloadClusterK8sClient(gomock.Any()).Times(0)

			r := &VmiEvictionReconciler{Client: fakeClient, workloadCluster: wlCluster}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: nodeName}}

			Expect(r.Reconcile(gocontext.TODO(), req)).Should(Equal(ctrl.Result{}))
		})

		It("Should ignore vmi if its deletion process already started", func() {
			es := kubevirtv1.EvictionStrategyExternal
			vmi.Spec.EvictionStrategy = &es
			vmi.Status.EvacuationNodeName = nodeName
			now := metav1.Now()
			vmi.DeletionTimestamp = &now

			fakeClient = fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(vmi, cluster).Build()

			// make sure we never get into darin process, but exit earlier
			wlCluster.EXPECT().GenerateWorkloadClusterK8sClient(gomock.Any()).Times(0)

			r := &VmiEvictionReconciler{Client: fakeClient, workloadCluster: wlCluster}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: nodeName}}

			Expect(r.Reconcile(gocontext.TODO(), req)).Should(Equal(ctrl.Result{}))
		})

		It("Should ignore vmi with no eviction strategy", func() {
			vmi.Spec.EvictionStrategy = nil
			vmi.Status.EvacuationNodeName = nodeName

			fakeClient = fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(vmi, cluster).Build()

			// make sure we never get into darin process, but exit earlier
			wlCluster.EXPECT().GenerateWorkloadClusterK8sClient(gomock.Any()).Times(0)

			r := &VmiEvictionReconciler{Client: fakeClient, workloadCluster: wlCluster}

			req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: nodeName}}

			Expect(r.Reconcile(gocontext.TODO(), req)).Should(Equal(ctrl.Result{}))
		})

		It("Should ignore vmi with no eviction strategy != external", func() {
			es := kubevirtv1.EvictionStrategyLiveMigrate
			vmi.Spec.EvictionStrategy = &es
			vmi.Status.EvacuationNodeName = nodeName

			fakeClient = fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(vmi, cluster).Build()

			// make sure we never get into darin process, but exit earlier
			wlCluster.EXPECT().GenerateWorkloadClusterK8sClient(gomock.Any()).Times(0)

			r := &VmiEvictionReconciler{Client: fakeClient, workloadCluster: wlCluster}

			req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: nodeName}}

			Expect(r.Reconcile(gocontext.TODO(), req)).Should(Equal(ctrl.Result{}))
		})

		It("Should ignore non-evicted VMIs", func() {
			es := kubevirtv1.EvictionStrategyExternal
			vmi.Spec.EvictionStrategy = &es

			fakeClient = fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(vmi, cluster).Build()

			// make sure we never get into darin process, but exit earlier
			wlCluster.EXPECT().GenerateWorkloadClusterK8sClient(gomock.Any()).Times(0)

			r := &VmiEvictionReconciler{Client: fakeClient, workloadCluster: wlCluster}

			req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: nodeName}}

			Expect(r.Reconcile(gocontext.TODO(), req)).Should(Equal(ctrl.Result{}))
		})

		It("Should drain node", func() {
			es := kubevirtv1.EvictionStrategyExternal
			vmi.Spec.EvictionStrategy = &es
			vmi.Status.EvacuationNodeName = nodeName

			fakeClient = fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(vmi, cluster).Build()

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
				},
			}

			Expect(k8sfake.AddToScheme(setupRemoteScheme())).ToNot(HaveOccurred())
			cl := k8sfake.NewSimpleClientset(node)

			wlCluster.EXPECT().GenerateWorkloadClusterK8sClient(gomock.Any()).Return(cl, nil).Times(1)

			r := &VmiEvictionReconciler{Client: fakeClient, workloadCluster: wlCluster}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: nodeName}}

			Expect(r.Reconcile(gocontext.TODO(), req)).Should(Equal(ctrl.Result{}))

			// check that the node was drained
			readNode, err := cl.CoreV1().Nodes().Get(gocontext.TODO(), nodeName, metav1.GetOptions{})
			Expect(err).ShouldNot(HaveOccurred())
			Expect(readNode.Spec.Unschedulable).To(BeTrue())

			// check that the VMI was removed
			readVMI := &kubevirtv1.VirtualMachineInstance{}
			err = fakeClient.Get(gocontext.TODO(), client.ObjectKey{Namespace: clusterNamespace, Name: nodeName}, readVMI)
			Expect(apierrors.IsNotFound(err)).Should(BeTrue())
		})

		It("Should skip drain if the node already deleted", func() {
			es := kubevirtv1.EvictionStrategyExternal
			vmi.Spec.EvictionStrategy = &es

			vmi.Status.EvacuationNodeName = nodeName
			fakeClient = fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(vmi, cluster).Build()

			Expect(k8sfake.AddToScheme(setupRemoteScheme())).ToNot(HaveOccurred())
			cl := k8sfake.NewSimpleClientset()

			wlCluster.EXPECT().GenerateWorkloadClusterK8sClient(gomock.Any()).Return(cl, nil).Times(1)

			r := &VmiEvictionReconciler{Client: fakeClient, workloadCluster: wlCluster}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: nodeName}}

			Expect(r.Reconcile(gocontext.TODO(), req)).Should(Equal(ctrl.Result{}))
		})

		Context("Error cases", func() {
			It("Should return error if the 'capk.cluster.x-k8s.io/kubevirt-machine-namespace' label is missing", func() {
				es := kubevirtv1.EvictionStrategyExternal
				vmi.Spec.EvictionStrategy = &es
				vmi.Status.EvacuationNodeName = nodeName
				delete(vmi.Labels, infrav1.KubevirtMachineNamespaceLabel)
				fakeClient = fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(vmi, cluster).Build()

				// make sure we never get into darin process, but exit earlier
				wlCluster.EXPECT().GenerateWorkloadClusterK8sClient(gomock.Any()).Times(0)

				r := &VmiEvictionReconciler{Client: fakeClient, workloadCluster: wlCluster}
				req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: nodeName}}
				_, err := r.Reconcile(gocontext.TODO(), req)
				Expect(err).Should(HaveOccurred())
			})

			It("Should return error if the 'cluster.x-k8s.io/cluster-name' label is missing", func() {
				es := kubevirtv1.EvictionStrategyExternal
				vmi.Spec.EvictionStrategy = &es
				vmi.Status.EvacuationNodeName = nodeName
				delete(vmi.Labels, clusterv1.ClusterLabelName)
				fakeClient = fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(vmi, cluster).Build()

				// make sure we never get into darin process, but exit earlier
				wlCluster.EXPECT().GenerateWorkloadClusterK8sClient(gomock.Any()).Times(0)

				r := &VmiEvictionReconciler{Client: fakeClient, workloadCluster: wlCluster}
				req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: nodeName}}
				_, err := r.Reconcile(gocontext.TODO(), req)
				Expect(err).Should(HaveOccurred())
			})

			It("Should return error if the cluster is missing", func() {
				es := kubevirtv1.EvictionStrategyExternal
				vmi.Spec.EvictionStrategy = &es
				vmi.Status.EvacuationNodeName = nodeName

				fakeClient = fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(vmi).Build()

				// make sure we never get into darin process, but exit earlier
				wlCluster.EXPECT().GenerateWorkloadClusterK8sClient(gomock.Any()).Times(0)

				r := &VmiEvictionReconciler{Client: fakeClient, workloadCluster: wlCluster}
				req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: nodeName}}
				_, err := r.Reconcile(gocontext.TODO(), req)
				Expect(err).Should(HaveOccurred())
			})

			It("Should return not error if can't get the external cluster client, but do not remove the VMI", func() {
				es := kubevirtv1.EvictionStrategyExternal
				vmi.Spec.EvictionStrategy = &es
				vmi.Status.EvacuationNodeName = nodeName

				fakeClient = fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(vmi, cluster).Build()

				Expect(k8sfake.AddToScheme(setupRemoteScheme())).ToNot(HaveOccurred())

				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName,
					},
				}

				Expect(k8sfake.AddToScheme(setupRemoteScheme())).ToNot(HaveOccurred())
				cl := k8sfake.NewSimpleClientset(node)

				wlCluster.EXPECT().GenerateWorkloadClusterK8sClient(gomock.Any()).Return(nil, errors.New("fake error")).Times(1)

				r := &VmiEvictionReconciler{Client: fakeClient, workloadCluster: wlCluster}
				req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "test-cluster", Name: nodeName}}

				_, err := r.Reconcile(gocontext.TODO(), req)
				Expect(err).ShouldNot(HaveOccurred())

				// check that the node was not drained
				readNode, err := cl.CoreV1().Nodes().Get(gocontext.TODO(), nodeName, metav1.GetOptions{})
				Expect(err).ShouldNot(HaveOccurred())
				Expect(readNode.Spec.Unschedulable).To(BeFalse())

				// check that the VMI was not deleted
				readVMI := &kubevirtv1.VirtualMachineInstance{}
				err = fakeClient.Get(gocontext.TODO(), client.ObjectKey{Namespace: clusterNamespace, Name: nodeName}, readVMI)
				Expect(err).ShouldNot(HaveOccurred())
				Expect(readVMI).ToNot(BeNil())
			})
		})
	})

	Context("check the label predicate", func() {
		sel, err := getLabelPredicate()
		It("should successfully create the predicate", func() {
			Expect(err).ToNot(HaveOccurred())
		})

		It("should select if the label exist", func() {
			Expect(sel.Create(event.CreateEvent{
				Object: &kubevirtv1.VirtualMachineInstance{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{infrav1.KubevirtMachineNameLabel: "machine-name"},
					},
				},
			})).To(BeTrue())
		})

		It("should select if the label exist and empty", func() {
			Expect(sel.Create(event.CreateEvent{
				Object: &kubevirtv1.VirtualMachineInstance{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{infrav1.KubevirtMachineNameLabel: ""},
					},
				},
			})).To(BeTrue())
		})

		It("should select if the label does not exist", func() {
			Expect(sel.Create(event.CreateEvent{
				Object: &kubevirtv1.VirtualMachineInstance{
					ObjectMeta: metav1.ObjectMeta{
						Labels: nil,
					},
				},
			})).To(BeFalse())
		})

		It("should select if the label exist", func() {
			Expect(sel.Update(event.UpdateEvent{
				ObjectOld: &kubevirtv1.VirtualMachineInstance{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{infrav1.KubevirtMachineNameLabel: "machine-name"},
					},
				},
				ObjectNew: &kubevirtv1.VirtualMachineInstance{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{infrav1.KubevirtMachineNameLabel: "machine-name"},
					},
				},
			})).To(BeTrue())
		})

		It("should select if the label now exist", func() {
			Expect(sel.Update(event.UpdateEvent{
				ObjectOld: &kubevirtv1.VirtualMachineInstance{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"foo": "bar"},
					},
				},
				ObjectNew: &kubevirtv1.VirtualMachineInstance{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{infrav1.KubevirtMachineNameLabel: "machine-name"},
					},
				},
			})).To(BeTrue())

		})

		It("should select if the label now not exist", func() {
			Expect(sel.Update(event.UpdateEvent{
				ObjectOld: &kubevirtv1.VirtualMachineInstance{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{infrav1.KubevirtMachineNameLabel: "machine-name"},
					},
				},
				ObjectNew: &kubevirtv1.VirtualMachineInstance{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"foo": "bar"},
					},
				},
			})).To(BeFalse())
		})
	})
})

func setupRemoteScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		panic(err)
	}
	return s
}
