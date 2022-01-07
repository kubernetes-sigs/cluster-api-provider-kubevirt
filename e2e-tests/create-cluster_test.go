package e2e_tests_test

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	tests "sigs.k8s.io/cluster-api-provider-kubevirt/e2e-tests"
)

var _ = Describe("CreateCluster", func() {

	BeforeEach(func() {
		Expect(tests.KubectlPath).ToNot(Equal(""))
		Expect(tests.ClusterctlPath).ToNot(Equal(""))

	})

	It("test", func() {
		Expect(true).To(BeTrue())
	})
})
