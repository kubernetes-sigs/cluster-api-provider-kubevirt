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

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/kind/pkg/cluster/constants"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
)

// LoadBalancer manages the load balancer for a specific KubeVirt cluster.
type LoadBalancer struct {
	name            string
	service         *corev1.Service
	kubevirtCluster *infrav1.KubevirtCluster
	infraClient     runtimeclient.Client
	infraNamespace  string
}

// NewLoadBalancer returns a new helper for managing a mock load-balancer (using service).
func NewLoadBalancer(ctx *context.ClusterContext, client runtimeclient.Client, namespace string) (*LoadBalancer, error) {
	name := ctx.Cluster.Name + "-lb"
	// Look for the service that is mocking the load-balancer for the cluster.
	// Filter based on the label and the roles regardless of whether or not it is running.
	loadBalancer := &corev1.Service{}
	loadBalancerKey := runtimeclient.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}
	if err := client.Get(ctx.Context, loadBalancerKey, loadBalancer); err != nil {
		if apierrors.IsNotFound(err) {
			loadBalancer = nil
		} else {
			return nil, err
		}
	}

	return &LoadBalancer{
		name:            name,
		service:         loadBalancer,
		kubevirtCluster: ctx.KubevirtCluster,
		infraClient:     client,
		infraNamespace:  namespace,
	}, nil
}

// IsFound checks if load balancer already exists
func (l *LoadBalancer) IsFound() bool {
	return l.service != nil
}

// Create creates a service of ClusterIP type to serve as a load-balancer for the cluster.
func (l *LoadBalancer) Create(ctx *context.ClusterContext) error {
	// Skip creation if exists.
	if l.IsFound() {
		return fmt.Errorf("the load balancer service already exists")
	}

	lbService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: l.infraNamespace,
			Name:      l.name,
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{
					Port:       6443,
					Protocol:   corev1.ProtocolTCP,
					TargetPort: intstr.FromInt32(6443),
				},
			},
			Selector: map[string]string{
				"cluster.x-k8s.io/role":         constants.ControlPlaneNodeRoleValue,
				"cluster.x-k8s.io/cluster-name": ctx.Cluster.Name,
			},
		},
	}

	lbService.Labels = ctx.KubevirtCluster.Spec.ControlPlaneServiceTemplate.ObjectMeta.Labels
	lbService.Annotations = ctx.KubevirtCluster.Spec.ControlPlaneServiceTemplate.ObjectMeta.Annotations
	lbService.Spec.Type = ctx.KubevirtCluster.Spec.ControlPlaneServiceTemplate.Spec.Type

	mutateFn := func() (err error) {
		if lbService.Labels == nil {
			lbService.Labels = map[string]string{}
		}
		lbService.Labels[clusterv1.ClusterNameLabel] = ctx.Cluster.Name

		return nil
	}
	if _, err := ctrlutil.CreateOrUpdate(ctx.Context, l.infraClient, lbService, mutateFn); err != nil {
		return corev1.ErrIntOverflowGenerated
	}

	return nil
}

// IP returns ip address of the load balancer
func (l *LoadBalancer) IP(ctx *context.ClusterContext) (string, error) {
	loadBalancer := &corev1.Service{}
	loadBalancerKey := runtimeclient.ObjectKey{
		Namespace: l.infraNamespace,
		Name:      l.name,
	}
	if err := l.infraClient.Get(ctx.Context, loadBalancerKey, loadBalancer); err != nil {
		return "", err
	}

	if len(loadBalancer.Spec.ClusterIP) == 0 {
		return "", fmt.Errorf("the load balancer service is not ready yet")
	}

	return loadBalancer.Spec.ClusterIP, nil
}

// ExternalIP returns external ip address of the load balancer
func (l *LoadBalancer) ExternalIP(ctx *context.ClusterContext) (string, error) {
	loadBalancer := &corev1.Service{}
	loadBalancerKey := runtimeclient.ObjectKey{
		Namespace: l.infraNamespace,
		Name:      l.name,
	}
	if err := l.infraClient.Get(ctx.Context, loadBalancerKey, loadBalancer); err != nil {
		return "", err
	}

	if len(loadBalancer.Status.LoadBalancer.Ingress) == 0 {
		return "", fmt.Errorf("the load balancer external IP is not ready yet")
	}

	return loadBalancer.Status.LoadBalancer.Ingress[0].IP, nil
}

// Delete deletes load-balancer service.
func (l *LoadBalancer) Delete(ctx *context.ClusterContext) error {
	if !l.IsFound() {
		return nil
	}

	if err := l.infraClient.Delete(ctx, l.service); err != nil {
		return errors.Wrapf(err, "failed to delete load balancer service")
	}

	return nil
}
