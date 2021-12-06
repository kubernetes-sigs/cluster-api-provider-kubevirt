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

package loadbalancer

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1beta1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
	clusterutil "sigs.k8s.io/cluster-api/util"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/kind/pkg/cluster/constants"
)

//type lbCreator interface {
//	CreateExternalLoadBalancerNode(name, image, clusterLabel, listenAddress string, port int32) (*types.Node, error)
//}

// LoadBalancer manages the load balancer for a specific KubeVirt cluster.
type LoadBalancer struct {
	name            string
	service         *corev1.Service
	client          runtimeclient.Client
	kubevirtCluster *infrav1.KubevirtCluster
}

// NewLoadBalancer returns a new helper for managing a mock load-balancer (using service).
func NewLoadBalancer(ctx *context.ClusterContext, client runtimeclient.Client) (*LoadBalancer, error) {
	name := ctx.KubevirtCluster.Name + "-lb"
	// Look for the service that is mocking the load-balancer for the cluster.
	// Filter based on the label and the roles regardless of whether or not it is running.
	loadBalancer := &corev1.Service{}
	loadBalancerKey := runtimeclient.ObjectKey{
		Namespace: ctx.KubevirtCluster.Namespace,
		Name:      name,
	}
	if err := client.Get(ctx.Context, loadBalancerKey, loadBalancer); err != nil {
		if apierrors.IsNotFound(err) {
			loadBalancer = nil
			ctx.Logger.Info("No load balancer found")
		} else {
			return nil, err
		}
	}

	return &LoadBalancer{
		name:            name,
		service:         loadBalancer,
		client:          client,
		kubevirtCluster: ctx.KubevirtCluster,
	}, nil
}

// Create creates a service of ClusterIP type to serve as a load-balancer for the cluster.
func (l *LoadBalancer) Create(ctx *context.ClusterContext) error {
	// Skip creation if exists.
	if l.service != nil {
		return fmt.Errorf("the load balancer service already exists")
	}

	lbService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: l.kubevirtCluster.Namespace,
			Name:      l.name,
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{
					Port:       6443,
					Protocol:   corev1.ProtocolTCP,
					TargetPort: intstr.FromInt(6443),
				},
			},
			Selector: map[string]string{
				"cluster.x-k8s.io/role": constants.ControlPlaneNodeRoleValue,
			},
		},
	}
	mutateFn := func() (err error) {
		lbService.SetOwnerReferences(clusterutil.EnsureOwnerRef(
			lbService.OwnerReferences,
			metav1.OwnerReference{
				APIVersion: l.kubevirtCluster.APIVersion,
				Kind:       l.kubevirtCluster.Kind,
				Name:       l.kubevirtCluster.Name,
				UID:        l.kubevirtCluster.UID,
			}))
		return nil
	}
	if _, err := ctrlutil.CreateOrUpdate(ctx, l.client, lbService, mutateFn); err != nil {
		return corev1.ErrIntOverflowGenerated
	}

	return nil
}

func (l *LoadBalancer) IsFound() bool {
	return l.service != nil
}

func (l *LoadBalancer) IP(ctx *context.ClusterContext) (string, error) {
	loadBalancer := &corev1.Service{}
	loadBalancerKey := runtimeclient.ObjectKey{
		Namespace: l.kubevirtCluster.Namespace,
		Name:      l.name,
	}
	if err := l.client.Get(ctx.Context, loadBalancerKey, loadBalancer); err != nil {
		return "", err
	}

	if len(loadBalancer.Spec.ClusterIP) == 0 {
		return "", fmt.Errorf("the load balancer service is not ready yet")
	}

	return loadBalancer.Spec.ClusterIP, nil
}
