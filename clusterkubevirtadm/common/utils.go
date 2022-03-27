package common

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	k8sclient "k8s.io/client-go/kubernetes"
	cr "sigs.k8s.io/controller-runtime"
)

func SetNamespaceFlag(cmd *cobra.Command, namespace *string) {
	cmd.Flags().StringVarP(namespace, CmdParamServiceNamespace, "n", "", "guest cluster namespace (default <cluster-name>-ns).")

	if err := cmd.MarkFlagRequired(CmdParamServiceNamespace); err != nil {
		CmdLog("internal error;", err) // should never happen
		os.Exit(1)
	}

}

// CmdLog writes logs and errors to stderr. stdout is for the command output
func CmdLog(output ...interface{}) {
	fmt.Fprintln(os.Stderr, output...)
}

func CreateClient(cmd *cobra.Command) (*k8sclient.Clientset, error) {
	kubeconfig, _ := cmd.Flags().GetString("kubeconfig")
	if len(kubeconfig) > 0 {
		os.Setenv("KUBECONFIG", kubeconfig)
	}

	cfg, err := cr.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("no configuration has been provided, try setting KUBECONFIG environment variable, use the --kubeconfig parameter, or make sure the ${HOME}/.kube/config file exists with the right configurations")
	}

	return k8sclient.NewForConfig(cfg)
}
