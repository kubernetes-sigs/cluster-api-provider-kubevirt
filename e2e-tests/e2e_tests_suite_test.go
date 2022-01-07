package e2e_tests_test

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"testing"
)

func TestE2eTests(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "E2eTests Suite")
}
