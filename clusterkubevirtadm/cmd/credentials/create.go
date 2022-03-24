/*
Copyright 2021 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package credentials

import (
	"github.com/spf13/cobra"

	"sigs.k8s.io/cluster-api-provider-kubevirt/clusterkubevirtadm/common"
)

func NewCreateCommand() *cobra.Command {
	cmdCtx := cmdContext{}

	// createCredentialsCmd represents the credentials command
	createCredentialsCmd := &cobra.Command{
		Use:     "credentials",
		Aliases: []string{"cred", "creds"},
		Short:   "creates a namespace, serviceAccount, role and roleBinding.",
		Long: `creates a namespace, serviceAccount, role and roleBinding to be used by the cluster-api to manage KubeVirt virtual machines.

Run the command against the infra-cluster - the cluster where KubeVirt is running.		

Exists with error if the namespace is already exist.
`,
		SilenceUsage: true,
	}

	createCredentialsCmd.RunE = func(cmd *cobra.Command, args []string) error {
		client, err := common.CreateClient(createCredentialsCmd)
		if err != nil {
			return err
		}

		cmdCtx.Client = client

		return createOrUpdateResources(cmd.Context(), cmdCtx, clientOperationCreate)
	}

	common.SetNamespaceFlag(createCredentialsCmd, &cmdCtx.Namespace)

	return createCredentialsCmd
}
