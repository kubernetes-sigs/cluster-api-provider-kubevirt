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

package kubeconfig

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	clientcmdlatest "k8s.io/client-go/tools/clientcmd/api/latest"
	"sigs.k8s.io/yaml"

	"sigs.k8s.io/cluster-api-provider-kubevirt/clusterkubevirtadm/common"
)

type cmdContext struct {
	Client     k8sclient.Interface
	Namespace  string
	OutputFile string
}

func NewGetCommand() *cobra.Command {
	cmdCtx := cmdContext{}

	// getCredentialsCmd represents the credentials command
	getCredentialsCmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Get the kubeconfig file for the cluster credentials",
		Long: `retrieves a kubeconfig file, represents the serviceAccount

Run the command against the infra-cluster - the cluster where KubeVirt is running.`,
		SilenceUsage: true,
	}

	getCredentialsCmd.RunE = func(cmd *cobra.Command, args []string) error {
		client, err := common.CreateClient(getCredentialsCmd)
		if err != nil {
			return err
		}

		cmdCtx.Client = client

		return get(cmd.Context(), cmdCtx)
	}

	common.SetNamespaceFlag(getCredentialsCmd, &cmdCtx.Namespace)

	getCredentialsCmd.Flags().StringVarP(&cmdCtx.OutputFile, "output-kubeconfig", "o", "", "if exists, write the result kubeconfig to this file. Overrides if already exist")

	return getCredentialsCmd
}

func get(ctx context.Context, opts cmdContext) error {
	sa, err := getServiceAccount(ctx, opts)
	if err != nil {
		return fmt.Errorf("can't get service account; %w", err)
	}

	return createKubeconfig(ctx, opts, sa)
}

func getServiceAccount(ctx context.Context, cmdCtx cmdContext) (*corev1.ServiceAccount, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()

	// make sure that the related secrets were created for the new serviceAccount. Wait 10 seconds before giving up.
	ticker := time.Tick(time.Millisecond * 100)
	for {
		select {
		case <-ticker:
			sa, err := cmdCtx.Client.CoreV1().ServiceAccounts(cmdCtx.Namespace).Get(ctx, common.ServiceAccountName, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}

			if len(sa.Secrets) > 0 {
				return sa, nil
			}

		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

}

func createKubeconfig(ctx context.Context, cmdCtx cmdContext, sa *corev1.ServiceAccount) error {
	token, err := getToken(ctx, cmdCtx, sa)
	if err != nil {
		return err
	}

	loader := clientcmd.NewDefaultClientConfigLoadingRules()
	config, err := loader.Load()
	if err != nil {
		return fmt.Errorf("can't load current kubeconfig file: %w", err)
	}

	err = clientcmdapi.FlattenConfig(config)
	if err != nil {
		return fmt.Errorf("flatten failed: %w", err)
	}

	err = clientcmdapi.MinifyConfig(config)
	if err != nil {
		return fmt.Errorf("minify failed: %w", err)
	}

	userName := cmdCtx.Namespace + "-token-user"

	// update and rename context
	config.Contexts[cmdCtx.Namespace] = config.Contexts[config.CurrentContext].DeepCopy()
	config.Contexts[cmdCtx.Namespace].AuthInfo = userName
	config.Contexts[cmdCtx.Namespace].Namespace = cmdCtx.Namespace
	delete(config.Contexts, config.CurrentContext)
	config.CurrentContext = cmdCtx.Namespace

	config.AuthInfos = map[string]*clientcmdapi.AuthInfo{
		userName: {
			Token: string(token),
		},
	}

	json, err := runtime.Encode(clientcmdlatest.Codec, config)
	if err != nil {
		return fmt.Errorf("failed to encode the configuration: %w", err)
	}
	output, err := yaml.JSONToYAML(json)
	if err != nil {
		return fmt.Errorf("failed to create yaml configuration: %w", err)
	}

	// if the output-kubeconfig parameter is missing, write the kubeconfig to stdout
	if len(cmdCtx.OutputFile) == 0 {
		fmt.Println(string(output))
	} else {
		err = writeKubeconfigFile(cmdCtx, output)
		if err != nil {
			return err
		}
	}

	return nil
}

func writeKubeconfigFile(cmdCtx cmdContext, output []byte) error {
	file, err := os.OpenFile(cmdCtx.OutputFile, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open the output kubeconfig file: %w", err)
	}
	defer func() { _ = file.Close() }()

	_, err = fmt.Fprint(file, string(output))
	if err != nil {
		return fmt.Errorf("failed to write the output kubeconfig file: %w", err)
	}
	return nil
}

func getToken(ctx context.Context, cmdCtx cmdContext, sa *corev1.ServiceAccount) ([]byte, error) {
	requestedSecretName := sa.Name + "-token"
	for _, sn := range sa.Secrets {
		if strings.HasPrefix(sn.Name, requestedSecretName) {
			secret, err := cmdCtx.Client.CoreV1().Secrets(cmdCtx.Namespace).Get(ctx, sn.Name, metav1.GetOptions{})
			if err != nil {
				return nil, fmt.Errorf("can't get the secret, %w", err)
			}
			token, ok := secret.Data["token"]
			if !ok {
				return nil, fmt.Errorf("can't find the koken in the secret")
			}
			return token, nil
		}
	}
	return nil, fmt.Errorf("can't find secret %s", requestedSecretName)
}
