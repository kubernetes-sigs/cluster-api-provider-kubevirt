package e2e_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"os"

	"flag"
	"testing"
)

// Test suite required arguments
var KubectlPath string
var ClusterctlPath string
var WorkingDir string

// Initialize test required arguments
func init() {
	flag.StringVar(&KubectlPath, "kubectl-path", "", "Path to the kubectl binary")
	flag.StringVar(&ClusterctlPath, "clusterctl-path", "", "Path to the clusterctl binary")
	flag.StringVar(&WorkingDir, "working-dir", "", "Path used for e2e test files")
}

func TestE2E(t *testing.T) {
	// Make sure that valid arguments have been passed for this test suite run.
	if KubectlPath == "" {
		t.Fatal("kubectl-path required")
	} else if _, err := os.Stat(KubectlPath); os.IsNotExist(err) {
		t.Fatalf("invalid kubectl-path path: %s doesn't exist", KubectlPath)
	}
	if ClusterctlPath == "" {
		t.Fatal("clusterctl-path required")
	} else if _, err := os.Stat(ClusterctlPath); os.IsNotExist(err) {
		t.Fatalf("invalid clusterctl-path path: %s doesn't exist", ClusterctlPath)
	}
	if WorkingDir == "" {
		t.Fatal("working-dir required")
	} else if _, err := os.Stat(WorkingDir); os.IsNotExist(err) {
		t.Fatalf("invalid working-dir path: %s doesn't exist", WorkingDir)
	}

	RegisterFailHandler(Fail)
	RunSpecs(t, "E2E Suite")
}

var _ = BeforeSuite(func() {
	// parse test suite arguments
	flag.Parse()
})
