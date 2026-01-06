package crds_test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/crds"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCRDs(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CRDs Suite")
}

const crdName = "crd-name"

var _ = Describe("CRDs", func() {
	DescribeTable("version list", func(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition, expected []string) {
		s := runtime.NewScheme()
		Expect(apiextensionsv1.AddToScheme(s)).To(Succeed())
		cl := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(crd).Build()

		versions, err := crds.GetSupportedVersions(ctx, cl, crdName)
		Expect(err).NotTo(HaveOccurred())
		Expect(versions).To(Equal(expected))
	},
		Entry("should return empty list if not found", &apiextensionsv1.CustomResourceDefinition{
			TypeMeta: metav1.TypeMeta{
				Kind:       "CustomResourceDefinition",
				APIVersion: apiextensionsv1.SchemeGroupVersion.String(),
			},
		}, nil),
		Entry("should return empty list if no versions", &apiextensionsv1.CustomResourceDefinition{
			TypeMeta: metav1.TypeMeta{
				Kind:       "CustomResourceDefinition",
				APIVersion: apiextensionsv1.SchemeGroupVersion.String(),
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: crdName,
			},
		}, nil),
		Entry("should return list of one version", &apiextensionsv1.CustomResourceDefinition{
			TypeMeta: metav1.TypeMeta{
				Kind:       "CustomResourceDefinition",
				APIVersion: apiextensionsv1.SchemeGroupVersion.String(),
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: crdName,
			},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{
						Name: "v1beta1",
					},
				},
			},
		}, []string{"v1beta1"}),
		Entry("should return list of two version", &apiextensionsv1.CustomResourceDefinition{
			TypeMeta: metav1.TypeMeta{
				Kind:       "CustomResourceDefinition",
				APIVersion: apiextensionsv1.SchemeGroupVersion.String(),
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: crdName,
			},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{
						Name: "v1beta1",
					},
					{
						Name: "v1beta2",
					},
				},
			},
		}, []string{"v1beta1", "v1beta2"}),
	)
})
