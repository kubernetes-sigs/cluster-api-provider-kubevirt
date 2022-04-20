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

package cmd

import (
	"github.com/spf13/cobra"

	"sigs.k8s.io/cluster-api-provider-kubevirt/clusterkubevirtadm/cmd/apply"
	"sigs.k8s.io/cluster-api-provider-kubevirt/clusterkubevirtadm/cmd/create"
	"sigs.k8s.io/cluster-api-provider-kubevirt/clusterkubevirtadm/cmd/get"
	"sigs.k8s.io/cluster-api-provider-kubevirt/clusterkubevirtadm/common"
)

func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "clusterkubevirtadm",
		Short: "manage the kubevirt guest clusters",
		Long:  `clusterkubevirtadm is a command line application to manage the kubevirt guest and infra clusters.`,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
	}
	var err error
	if err != nil {
		common.CmdLog("can't connect to the cluster; ", err)
	}

	rootCmd.PersistentFlags().String("kubeconfig", "", "Path to the kubeconfig file to use for CLI requests.")

	rootCmd.AddCommand(create.NewCommand())
	rootCmd.AddCommand(get.NewCommand())
	rootCmd.AddCommand(apply.NewCommand())

	return rootCmd
}
