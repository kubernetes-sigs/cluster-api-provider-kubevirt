package apply

import (
	"github.com/spf13/cobra"

	"sigs.k8s.io/cluster-api-provider-kubevirt/clusterkubevirtadm/cmd/credentials"
)

func NewCommand() *cobra.Command {
	createCmd := &cobra.Command{
		Use:          "apply",
		Short:        "Commands for creating or updating cluster-api-provider-kubevirt related resources",
		SilenceUsage: true,
	}
	createCmd.AddCommand(credentials.NewApplyCommand())

	return createCmd
}
