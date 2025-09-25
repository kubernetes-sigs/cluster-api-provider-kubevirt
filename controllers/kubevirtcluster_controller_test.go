package controllers_test

import (
	goContext "context"
	"time"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/controllers"
	infraclustermock "sigs.k8s.io/cluster-api-provider-kubevirt/pkg/infracluster/mock"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/testing"
)

var (
	mockCtrl                  *gomock.Controller
	clusterName               string
	kubevirtClusterName       string
	kubevirtCluster           *infrav1.KubevirtCluster
	kubeconfigNamespace       string
	cluster                   *clusterv1.Cluster
	fakeClient                ctrlclient.Client
	kubevirtClusterReconciler controllers.KubevirtClusterReconciler
	fakeContext               = goContext.TODO()
	testLogger                = ctrl.Log.WithName("test")
	infraClusterMock          *infraclustermock.MockInfraCluster
)

var _ = Describe("Reconcile", func() {

	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		infraClusterMock = infraclustermock.NewMockInfraCluster(mockCtrl)
	})

	setupClient := func(objects []ctrlclient.Object) {
		fakeClient = ctrlfake.NewClientBuilder().
			WithScheme(testing.SetupScheme()).
			WithObjects(objects...).
			WithStatusSubresource(objects...).
			Build()
		kubevirtClusterReconciler = controllers.KubevirtClusterReconciler{
			Client:       fakeClient,
			InfraCluster: infraClusterMock,
			APIReader:    fakeClient,
			Log:          testLogger,
		}
	}

	Context("reconcile generic cluster", func() {
		BeforeEach(func() {
			clusterName = "test-cluster"
			kubevirtClusterName = "test-kubevirt-cluster"
			kubevirtCluster = testing.NewKubevirtCluster(kubevirtClusterName, kubevirtClusterName)
			cluster = testing.NewCluster(kubevirtClusterName, kubevirtCluster)
			objects := []ctrlclient.Object{
				cluster,
				kubevirtCluster,
			}
			fakeClient = ctrlfake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(objects...).Build()
		})

		AfterEach(func() {})

		It("should create cluster", func() {
			objects := []ctrlclient.Object{
				cluster,
				kubevirtCluster,
			}
			setupClient(objects)
			infraClusterMock.EXPECT().GenerateInfraClusterClient(gomock.Any(), gomock.Any(), gomock.Any()).Return(fakeClient, kubevirtCluster.Namespace, nil)

			result, err := kubevirtClusterReconciler.Reconcile(fakeContext, ctrl.Request{
				NamespacedName: ctrlclient.ObjectKey{
					Namespace: kubevirtCluster.Namespace,
					Name:      kubevirtCluster.Name,
				},
			})
			Expect(err).ShouldNot(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())
		})

		It("should not create cluster when namespace and kubevirtCluster is not specified", func() {
			result, err := kubevirtClusterReconciler.Reconcile(fakeContext, ctrl.Request{
				NamespacedName: ctrlclient.ObjectKey{
					Namespace: "",
					Name:      "",
				},
			})
			Expect(err).ShouldNot(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())
		})

		It("should not create cluster when invalid namespace and kubevirtCluster is specified", func() {
			result, err := kubevirtClusterReconciler.Reconcile(fakeContext, ctrl.Request{
				NamespacedName: ctrlclient.ObjectKey{
					Namespace: "Invalid Namespace",
					Name:      "Invalid Name",
				},
			})
			Expect(err).ShouldNot(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())
		})
	})

	Context("reconcile a cluster with finalizer set", func() {
		BeforeEach(func() {
			clusterName = "test-cluster"
			kubevirtClusterName = "test-kubevirt-cluster"
			kubevirtCluster = testing.NewKubevirtCluster(kubevirtClusterName, kubevirtClusterName)
			kubevirtCluster.Finalizers = []string{infrav1.ClusterFinalizer}
			cluster = testing.NewCluster(kubevirtClusterName, kubevirtCluster)
		})

		AfterEach(func() {})

		It("should throw an error when reconciling unhandled clusters. ", func() {
			objects := []ctrlclient.Object{
				cluster,
				kubevirtCluster,
			}
			setupClient(objects)
			infraClusterMock.EXPECT().GenerateInfraClusterClient(gomock.Any(), gomock.Any(), gomock.Any()).Return(fakeClient, kubevirtCluster.Namespace, nil)

			_, err := kubevirtClusterReconciler.Reconcile(fakeContext, ctrl.Request{
				NamespacedName: ctrlclient.ObjectKey{
					Namespace: kubevirtCluster.Namespace,
					Name:      kubevirtCluster.Name,
				},
			})
			// Load Balancer service is not ready yet.
			Expect(err).Should(HaveOccurred())
		})
	})

	Context("reconcile cluster with finalizer and deletion time stamp", func() {
		BeforeEach(func() {
			clusterName = "test-cluster"
			kubevirtClusterName = "test-kubevirt-cluster"
			kubevirtCluster = testing.NewKubevirtCluster(kubevirtClusterName, kubevirtClusterName)
			kubevirtCluster.Finalizers = []string{infrav1.ClusterFinalizer}
			kubevirtCluster.SetDeletionTimestamp(&metav1.Time{Time: time.Now()})
			cluster = testing.NewCluster(kubevirtClusterName, kubevirtCluster)
		})

		AfterEach(func() {})

		It("should succeed with the kubevirt cluster being deleted.", func() {
			objects := []ctrlclient.Object{
				cluster,
				kubevirtCluster,
			}
			setupClient(objects)
			infraClusterMock.EXPECT().GenerateInfraClusterClient(gomock.Any(), gomock.Any(), gomock.Any()).Return(fakeClient, kubevirtCluster.Namespace, nil)

			namespacedName := ctrlclient.ObjectKey{
				Namespace: kubevirtCluster.Namespace,
				Name:      kubevirtCluster.Name,
			}

			result, err := kubevirtClusterReconciler.Reconcile(fakeContext, ctrl.Request{
				NamespacedName: namespacedName,
			})
			Expect(err).ShouldNot(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			kvc := &infrav1.KubevirtCluster{}
			err = fakeClient.Get(fakeContext, namespacedName, kvc)
			Expect(err).Should(HaveOccurred())
		})
	})

	Context("Compute Control Plane LB service namespace values precedence before it's created", func() {
		BeforeEach(func() {
			clusterName = "test-cluster"
			kubevirtClusterName = "test-kubevirt-cluster"
			kubeconfigNamespace = "kubeconfig-namespace"
			cluster = testing.NewCluster(kubevirtClusterName, kubevirtCluster)
		})

		AfterEach(func() {})

		It("should use provided LB namespace if its set", func() {
			kubevirtCluster = testing.NewKubevirtClusterWithNamespacedLB(kubevirtClusterName, kubevirtClusterName, "lb-namespace")
			ns := controllers.GetLoadBalancerNamespace(kubevirtCluster, kubeconfigNamespace)
			Expect(ns).To(Equal("lb-namespace"))
		})
		It("should use kubeconfig namespace if LB namespace is not set", func() {
			kubevirtCluster = testing.NewKubevirtClusterWithNamespacedLB(kubevirtClusterName, kubevirtClusterName, "")
			ns := controllers.GetLoadBalancerNamespace(kubevirtCluster, kubeconfigNamespace)
			Expect(ns).To(Equal(kubeconfigNamespace))
		})
	})
})
