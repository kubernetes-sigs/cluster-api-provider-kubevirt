package kubeconfig

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestKubeconfigSuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "kubeconfig Suite")
}
