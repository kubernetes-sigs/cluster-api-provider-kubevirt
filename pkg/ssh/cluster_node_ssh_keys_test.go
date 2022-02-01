package ssh_test

import (
	gocontext "context"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubevirtv1 "kubevirt.io/api/core/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/ssh"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/testing"
)

var (
	clusterName         = "test-cluster"
	kubevirtClusterName = "test-kubevirt-cluster"
	kubevirtCluster     = testing.NewKubevirtCluster(clusterName, kubevirtClusterName)
	cluster             = testing.NewCluster(clusterName, kubevirtCluster)

	clusterContext = &context.ClusterContext{
		Logger:          ctrl.LoggerFrom(gocontext.TODO()).WithName("test"),
		Context:         gocontext.TODO(),
		Cluster:         cluster,
		KubevirtCluster: kubevirtCluster,
	}
	clusterNodeSshKeys ssh.ClusterNodeSshKeys
)
var _ = Describe("ClusterNodeSshKeys", func() {
	var (
		fakeClient client.Client
	)
	Context("when ssh keys have not been set to context yet", func() {
		BeforeEach(func() {
			objects := []client.Object{
				cluster,
				kubevirtCluster,
			}
			fakeClient = fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(objects...).Build()
			clusterNodeSshKeys = ssh.ClusterNodeSshKeys{
				Client:         fakeClient,
				ClusterContext: clusterContext,
			}
		})
		It("generate keys should successfully generate new keys", func() {
			err := clusterNodeSshKeys.GenerateNewKeys()
			Expect(err).NotTo(HaveOccurred())
		})
		It("persist keys fails to persist nil value to secret", func() {
			resp, errors := clusterNodeSshKeys.PersistKeysToSecret()
			Expect(resp).To(BeNil())
			Expect(errors).To(HaveOccurred())
		})
		It("get keys does not return a value and there is no error", func() {
			err, _ := clusterNodeSshKeys.GetKeysDataSecret()
			Expect(err).To(BeNil())
		})
		It("is persisted returns false", func() {
			result := clusterNodeSshKeys.IsPersistedToSecret()
			Expect(result).To(BeFalse())
		})
		It("fetch persisted keys from secret returns an error", func() {
			err := clusterNodeSshKeys.FetchPersistedKeysFromSecret()
			Expect(err).To(HaveOccurred())
		})
	})

	Context("when ssh keys is set to context", func() {
		BeforeEach(func() {
			objects := []client.Object{
				cluster,
				kubevirtCluster,
			}
			fakeClient = fake.NewClientBuilder().WithScheme(setupScheme()).WithObjects(objects...).Build()
			clusterNodeSshKeys = ssh.ClusterNodeSshKeys{
				Client:         fakeClient,
				ClusterContext: clusterContext,
			}
		})
		It("should generate, persist, fetch keys succeeds", func() {
			err := clusterNodeSshKeys.GenerateNewKeys()
			Expect(err).NotTo(HaveOccurred())
			sshKeysDataSecret, err := clusterNodeSshKeys.PersistKeysToSecret()
			Expect(err).NotTo(HaveOccurred())
			Expect(sshKeysDataSecret.Name).To(Equal("test-kubevirt-cluster-ssh-keys"))
			clusterContext.KubevirtCluster.Spec.SshKeys = infrav1.SSHKeys{
				ConfigRef: &corev1.ObjectReference{
					APIVersion: sshKeysDataSecret.APIVersion,
					Kind:       sshKeysDataSecret.Kind,
					Name:       sshKeysDataSecret.Name,
					Namespace:  sshKeysDataSecret.Namespace,
					UID:        sshKeysDataSecret.UID,
				},
				DataSecretName: &sshKeysDataSecret.Name,
			}
			secret, _ := clusterNodeSshKeys.GetKeysDataSecret()
			Expect(secret).NotTo(BeNil())
			result := clusterNodeSshKeys.IsPersistedToSecret()
			Expect(result).To(BeTrue())
			_ = clusterNodeSshKeys.FetchPersistedKeysFromSecret()
		})
	})
})

func setupScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	if err := clusterv1.AddToScheme(s); err != nil {
		panic(err)
	}
	if err := infrav1.AddToScheme(s); err != nil {
		panic(err)
	}
	if err := kubevirtv1.AddToScheme(s); err != nil {
		panic(err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		panic(err)
	}
	return s
}
