package e2e_test

import (
	"fmt"
	"sync"

	"k8s.io/client-go/kubernetes"
	kubevirtv1 "kubevirt.io/api/core/v1"
	kubeadmv1beta2 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
)

var (
	k8sclient    client.Client
	k8sClientSet *kubernetes.Clientset

	clientsOnce = &sync.Once{}
)

func initClients() error {
	var err error
	clientsOnce.Do(func() {
		cfg, doErr := config.GetConfig()
		if doErr != nil {
			err = fmt.Errorf("failed to get config; %w", doErr)
			return
		}

		k8sclient, err = client.New(cfg, client.Options{})
		if err != nil {
			err = fmt.Errorf("failed to create client; %w", err)
			return
		}

		k8sClientSet, err = kubernetes.NewForConfig(cfg)
		if err != nil {
			err = fmt.Errorf("failed to create clientset; %w", err)
			return
		}

		s := k8sclient.Scheme()
		err = clusterv1.AddToScheme(s)
		if err != nil {
			err = fmt.Errorf("failed to setup scheme for clusterv1; %w", err)
			return
		}

		err = infrav1.AddToScheme(s)
		if err != nil {
			err = fmt.Errorf("failed to setup scheme for infrav1; %w", err)
			return
		}

		err = kubevirtv1.AddToScheme(s)
		if err != nil {
			err = fmt.Errorf("failed to setup scheme for kubevirtv1; %w", err)
		}

		err = kubeadmv1beta2.AddToScheme(s)
		if err != nil {
			err = fmt.Errorf("failed to setup scheme for kubeadmv1beta2; %w", err)
		}
	})

	return err
}
