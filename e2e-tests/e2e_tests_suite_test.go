package e2e_tests_test

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"testing"

	tests "sigs.k8s.io/cluster-api-provider-kubevirt/e2e-tests"
)

func TestE2eTests(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "E2eTests Suite")
}

var _ = BeforeSuite(func() {
	tests.BeforeTestSuiteSetup()
})

var _ = AfterSuite(func() {
	tests.AfterTestSuiteCleanup()
})
