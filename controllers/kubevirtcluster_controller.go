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

package controllers

import (
	gocontext "context"
	"time"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/predicates"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/infracluster"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/loadbalancer"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/ssh"
)

// KubevirtClusterReconciler reconciles a KubevirtCluster object.
type KubevirtClusterReconciler struct {
	client.Client
	InfraCluster infracluster.InfraCluster
	Log          logr.Logger
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kubevirtclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kubevirtclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=services;,verbs=get;list;watch;create;update;patch;delete

// Reconcile reads that state of the cluster for a KubevirtCluster object and makes changes based on the state read
// and what is in the KubevirtCluster.Spec.
func (r *KubevirtClusterReconciler) Reconcile(goctx gocontext.Context, req ctrl.Request) (_ ctrl.Result, rerr error) {
	log := ctrl.LoggerFrom(goctx)

	// Fetch the KubevirtCluster.
	kubevirtCluster := &infrav1.KubevirtCluster{}
	if err := r.Client.Get(goctx, req.NamespacedName, kubevirtCluster); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Fetch the Cluster.
	cluster, err := util.GetOwnerCluster(goctx, r.Client, kubevirtCluster.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if cluster == nil {
		log.Info("Waiting for Cluster Controller to set OwnerRef on KubevirtCluster")
		return ctrl.Result{}, nil
	}

	log = log.WithValues("cluster", cluster.Name)

	// Create the cluster context for this request.
	clusterContext := &context.ClusterContext{
		Context:         goctx,
		Cluster:         cluster,
		KubevirtCluster: kubevirtCluster,
		Logger:          ctrl.LoggerFrom(goctx).WithName(req.Namespace).WithName(req.Name),
	}

	infraClusterClient, infraClusterNamespace, err := r.InfraCluster.GenerateInfraClusterClient(kubevirtCluster.Spec.InfraClusterSecretRef, kubevirtCluster.Namespace, goctx)
	if err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, errors.Wrap(err, "failed to generate infra cluster client")
	}
	if infraClusterClient == nil {
		clusterContext.Logger.Info("Waiting for infra cluster client...")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Create a helper for managing a service hosting the load-balancer.
	externalLoadBalancer, err := loadbalancer.NewLoadBalancer(clusterContext, infraClusterClient, infraClusterNamespace)
	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to create helper for managing the externalLoadBalancer")
	}

	// Initialize the patch helper
	patchHelper, err := patch.NewHelper(kubevirtCluster, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	// Always attempt to Patch the KubevirtCluster object and status after each reconciliation.
	defer func() {
		if err := clusterContext.PatchKubevirtCluster(patchHelper); err != nil {
			clusterContext.Logger.Error(err, "failed to patch KubevirtCluster")
			if rerr == nil {
				rerr = err
			}
		}
	}()

	// Add finalizer first if does not exist to avoid the race condition between init and delete
	if !controllerutil.ContainsFinalizer(kubevirtCluster, infrav1.ClusterFinalizer) {
		controllerutil.AddFinalizer(kubevirtCluster, infrav1.ClusterFinalizer)
		return ctrl.Result{}, nil
	}

	// Handle deleted clusters
	if !kubevirtCluster.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(clusterContext, externalLoadBalancer)
	}

	// Handle non-deleted clusters
	return r.reconcileNormal(clusterContext, externalLoadBalancer)
}

func (r *KubevirtClusterReconciler) reconcileNormal(ctx *context.ClusterContext, externalLoadBalancer *loadbalancer.LoadBalancer) (ctrl.Result, error) {
	// Create the service serving as load balancer, if not existing
	if !externalLoadBalancer.IsFound() {
		if err := externalLoadBalancer.Create(ctx); err != nil {
			conditions.MarkFalse(ctx.KubevirtCluster, infrav1.LoadBalancerAvailableCondition, infrav1.LoadBalancerProvisioningFailedReason, clusterv1.ConditionSeverityWarning, err.Error())
			return ctrl.Result{}, errors.Wrap(err, "failed to create load balancer")
		}
	}

	// Get LoadBalancer ExternalIP if cluster Service Type is LoadBalancer
	if ctx.KubevirtCluster.Spec.ControlPlaneServiceTemplate.Spec.Type == "LoadBalancer" {
		lbip4, err := externalLoadBalancer.ExternalIP(ctx)
		if err != nil {
			conditions.MarkFalse(ctx.KubevirtCluster, infrav1.LoadBalancerAvailableCondition, infrav1.LoadBalancerProvisioningFailedReason, clusterv1.ConditionSeverityWarning, err.Error())
			return ctrl.Result{}, errors.Wrap(err, "failed to get ExternalIP for the load balancer")
		}
		ctx.KubevirtCluster.Spec.ControlPlaneEndpoint = infrav1.APIEndpoint{
			Host: lbip4,
			Port: 6443,
		}

	// Get Cluster IP if cluster Service Type is CusterIP
	} else {
		lbip4, err := externalLoadBalancer.IP(ctx)
		if err != nil {
			conditions.MarkFalse(ctx.KubevirtCluster, infrav1.LoadBalancerAvailableCondition, infrav1.LoadBalancerProvisioningFailedReason, clusterv1.ConditionSeverityWarning, err.Error())
			return ctrl.Result{}, errors.Wrap(err, "failed to get ClusterIP for the load balancer")
		}
		ctx.KubevirtCluster.Spec.ControlPlaneEndpoint = infrav1.APIEndpoint{
			Host: lbip4,
			Port: 6443,
		}
	}

	conditions.MarkTrue(ctx.KubevirtCluster, infrav1.LoadBalancerAvailableCondition)

	// Generate ssh keys for cluster nodes, and persist them to a secret
	clusterNodeSSHKeys := ssh.NewClusterNodeSshKeys(ctx, r.Client)
	if !clusterNodeSSHKeys.IsPersistedToSecret() {
		if err := clusterNodeSSHKeys.GenerateNewKeys(); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to generate new ssh keys")
		}
		if sshKeysDataSecret, err := clusterNodeSSHKeys.PersistKeysToSecret(); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to persist ssh keys to secret")
		} else {
			ctx.KubevirtCluster.Spec.SshKeys = infrav1.SSHKeys{
				ConfigRef: &corev1.ObjectReference{
					APIVersion: sshKeysDataSecret.APIVersion,
					Kind:       sshKeysDataSecret.Kind,
					Name:       sshKeysDataSecret.Name,
					Namespace:  sshKeysDataSecret.Namespace,
					UID:        sshKeysDataSecret.UID,
				},
				DataSecretName: &sshKeysDataSecret.Name,
			}
		}
	}

	// Mark the KubevirtCluster ready
	ctx.KubevirtCluster.Status.Ready = true

	return ctrl.Result{}, nil
}

func (r *KubevirtClusterReconciler) reconcileDelete(ctx *context.ClusterContext, externalLoadBalancer *loadbalancer.LoadBalancer) (ctrl.Result, error) {
	ctx.Logger.Info("Deleting load balancer service...")
	if err := externalLoadBalancer.Delete(ctx); err != nil {
		ctx.Logger.Error(err, "Failed to delete load balancer service.")
	}

	// Set the LoadBalancerAvailableCondition reporting delete is started, and issue a patch in order to make
	// this visible to the users.
	patchHelper, err := patch.NewHelper(ctx.KubevirtCluster, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	conditions.MarkFalse(ctx.KubevirtCluster, infrav1.LoadBalancerAvailableCondition, clusterv1.DeletingReason, clusterv1.ConditionSeverityInfo, "")
	if err := ctx.PatchKubevirtCluster(patchHelper); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to patch KubevirtCluster")
	}

	// Cluster is deleted so remove the finalizer.
	controllerutil.RemoveFinalizer(ctx.KubevirtCluster, infrav1.ClusterFinalizer)

	return ctrl.Result{}, nil
}

// SetupWithManager will add watches for this controller.
func (r *KubevirtClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c, err := ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.KubevirtCluster{}).
		WithEventFilter(predicates.ResourceNotPaused(r.Log)).
		WithEventFilter(predicates.ResourceIsNotExternallyManaged(r.Log)).
		Build(r)
	if err != nil {
		return err
	}
	return c.Watch(
		&source.Kind{Type: &clusterv1.Cluster{}},
		handler.EnqueueRequestsFromMapFunc(util.ClusterToInfrastructureMapFunc(infrav1.GroupVersion.WithKind("KubevirtCluster"))),
		predicates.ClusterUnpaused(r.Log),
	)
}
