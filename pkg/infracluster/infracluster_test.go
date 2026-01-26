package infracluster_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	. "sigs.k8s.io/cluster-api-provider-kubevirt/pkg/infracluster"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/testing"
)

var (
	fakeClient         client.Client
	infraClusterSecret *corev1.Secret
	ownerNamespace     = "Mordor"
	infraSecretName    = "external-infra-kubeconfig"
	kubeconfig         = `apiVersion: v1
clusters:
- cluster:
    insecure-skip-tls-verify: true
    server: https://gondor.com
  name: gondor
contexts:
- context:
    cluster: gondor
    namespace: minastirith
    user: aragorn
  name: gondor
current-context: gondor
kind: Config
preferences: {}
users:
- name: aragorn
`
)

var _ = Describe("InfraCluster", func() {

	It("should return the management client and namespace when the infrastructure secret reference is nil", func() {
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).Build()

		infraCluster := New(fakeClient, fakeClient)
		infraClient, infraNamespace, err := infraCluster.GenerateInfraClusterClient(nil, ownerNamespace, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(infraClient).To(BeIdenticalTo(fakeClient))
		Expect(infraNamespace).To(Equal(ownerNamespace))
	})

	It("should failed when the referenced infrastructure secret cannot be found", func() {
		fakeClient := fake.NewClientBuilder().WithScheme(testing.SetupScheme()).Build()

		infraClusterSecretRef := &corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Secret",
			Name:       infraSecretName,
		}
		infraCluster := New(fakeClient, nil)

		_, _, err := infraCluster.GenerateInfraClusterClient(infraClusterSecretRef, ownerNamespace, nil)
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})

	It("should fail when the referenced infrastructure secret doesn't have a kubeconfig data in it", func() {
		infraClusterSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      infraSecretName,
				Namespace: ownerNamespace,
			},
			Data: map[string][]byte{},
		}
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(infraClusterSecret).Build()

		infraClusterSecretRef := &corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Secret",
			Name:       infraSecretName,
		}

		infraCluster := New(fakeClient, nil)
		_, _, err := infraCluster.GenerateInfraClusterClient(infraClusterSecretRef, ownerNamespace, nil)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(Equal("failed to retrieve infra kubeconfig from secret: 'kubeconfig' key is missing"))
	})

	It("should fail when the referenced infrastructure secret kubeconfig data is invalid", func() {
		infraClusterSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      infraSecretName,
				Namespace: ownerNamespace,
			},
			Data: map[string][]byte{
				"kubeconfig": []byte("hello world"),
			},
		}
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(infraClusterSecret).Build()

		infraClusterSecretRef := &corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Secret",
			Name:       infraSecretName,
		}

		infraCluster := New(fakeClient, nil)
		_, _, err := infraCluster.GenerateInfraClusterClient(infraClusterSecretRef, ownerNamespace, nil)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("failed to create K8s-API client config"))
	})

	It("should return the infra-client and the namespace defined in the secret, when set", func() {

		infraClusterSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      infraSecretName,
				Namespace: ownerNamespace,
			},
			Data: map[string][]byte{
				"kubeconfig": []byte(kubeconfig),
				"namespace":  []byte("Shire"),
			},
		}
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(infraClusterSecret).Build()

		infraClusterSecretRef := &corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Secret",
			Name:       infraSecretName,
		}

		fakeInfraClient := fake.NewClientBuilder().Build()
		infraCluster := NewWithFactory(fakeClient, nil,
			func(config *rest.Config, options client.Options) (client.Client, error) {
				return fakeInfraClient, nil
			},
		)
		infraClient, namespace, err := infraCluster.GenerateInfraClusterClient(infraClusterSecretRef, ownerNamespace, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(infraClient).To(BeIdenticalTo(fakeInfraClient))
		Expect(namespace).To(Equal("Shire"))
	})

	It("should return the infra-client and kubeconfig namespace when the secret doesn't specified one", func() {

		infraClusterSecret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      infraSecretName,
				Namespace: ownerNamespace,
			},
			Data: map[string][]byte{
				"kubeconfig": []byte(kubeconfig),
			},
		}
		fakeClient = fake.NewClientBuilder().WithScheme(testing.SetupScheme()).WithObjects(infraClusterSecret).Build()

		infraClusterSecretRef := &corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Secret",
			Name:       infraSecretName,
		}

		fakeInfraClient := fake.NewClientBuilder().Build()
		infraCluster := NewWithFactory(fakeClient, nil,
			func(config *rest.Config, options client.Options) (client.Client, error) {
				return fakeInfraClient, nil
			},
		)
		infraClient, namespace, err := infraCluster.GenerateInfraClusterClient(infraClusterSecretRef, ownerNamespace, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(infraClient).To(BeIdenticalTo(fakeInfraClient))
		Expect(namespace).To(Equal("minastirith"))
	})

})
