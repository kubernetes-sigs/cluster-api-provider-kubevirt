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
	"fmt"
	"slices"
	"time"

	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1" //nolint SA1019
	"sigs.k8s.io/controller-runtime/pkg/builder"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/predicates"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/capiv1beta1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/crds"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/infracluster"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/loadbalancer"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/ssh"
)

const (
	clusterCRDName = "clusters.cluster.x-k8s.io"
)

// KubevirtClusterReconciler reconciles a KubevirtCluster object.
type KubevirtClusterReconciler struct {
	client.Client
	// APIReader is used to prune the Cloud Controller resources for the given cluster:
	// this client doesn't locally cache the resources upon a GET/LIST request,
	// decreasing memory consumption and avoiding granting further RBAC verbs.
	APIReader       client.Reader
	InfraCluster    infracluster.InfraCluster
	Log             logr.Logger
	GetOwnerCluster func(ctx gocontext.Context, c client.Client, obj metav1.ObjectMeta) (*clusterv1.Cluster, error)
}

func GetLoadBalancerNamespace(kc *infrav1.KubevirtCluster, infraClusterNamespace string) string {
	// Use namespace specified in Service Template if exist
	if kc.Spec.ControlPlaneServiceTemplate.ObjectMeta.Namespace != "" {
		return kc.Spec.ControlPlaneServiceTemplate.ObjectMeta.Namespace
	}
	return infraClusterNamespace
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kubevirtclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kubevirtclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=services;,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts;configmaps,verbs=delete;list
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=delete;list
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=delete;list
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list

// Reconcile reads that state of the cluster for a KubevirtCluster object and makes changes based on the state read
// and what is in the KubevirtCluster.Spec.
func (r *KubevirtClusterReconciler) Reconcile(goctx gocontext.Context, req ctrl.Request) (_ ctrl.Result, rerr error) {
	log := ctrl.LoggerFrom(goctx)
	log.Info("Reconciling KubevirtCluster")

	// Fetch the KubevirtCluster.
	kubevirtCluster := &infrav1.KubevirtCluster{}
	if err := r.Get(goctx, req.NamespacedName, kubevirtCluster); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if annotations.IsExternallyManaged(kubevirtCluster) {
		log.V(4).Info(fmt.Sprintf("KubevirtCluster %s/%s is externally managed, will not attempt to reconcile object.", kubevirtCluster.Namespace, kubevirtCluster.Name))
		return ctrl.Result{}, nil
	}

	// Fetch the Cluster.
	cluster, err := r.GetOwnerCluster(goctx, r.Client, kubevirtCluster.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if cluster == nil {
		log.Info("Waiting for Cluster Controller to set OwnerRef on KubevirtCluster")
		return ctrl.Result{}, nil
	}

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

	loadBalancerNamespace := GetLoadBalancerNamespace(kubevirtCluster, infraClusterNamespace)

	// Create a helper for managing a service hosting the load-balancer.
	externalLoadBalancer, err := loadbalancer.NewLoadBalancer(clusterContext, infraClusterClient, loadBalancerNamespace)
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
			if err = r.filterOutNotFoundError(err); err != nil {

				clusterContext.Logger.Error(err, "failed to patch KubevirtCluster")
				if rerr == nil {
					rerr = err
				}
			}
		}
	}()

	// Add finalizer first if it does not exist to avoid the race condition between init and delete
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
			conditions.Set(ctx.KubevirtCluster, metav1.Condition{
				Type:    infrav1.LoadBalancerAvailableCondition,
				Status:  metav1.ConditionFalse,
				Reason:  infrav1.LoadBalancerProvisioningFailedReason,
				Message: fmt.Sprintf("%v", err.Error()),
			})
			return ctrl.Result{}, errors.Wrap(err, "failed to create load balancer")
		}
	}

	// Get the ControlPlane Host and Port manually set by the user if existing
	if ctx.KubevirtCluster.Spec.ControlPlaneEndpoint.Host != "" {
		ctx.KubevirtCluster.Spec.ControlPlaneEndpoint = infrav1.APIEndpoint{
			Host: ctx.KubevirtCluster.Spec.ControlPlaneEndpoint.Host,
			Port: ctx.KubevirtCluster.Spec.ControlPlaneEndpoint.Port,
		}
		// Get LoadBalancer ExternalIP if cluster Service Type is LoadBalancer
	} else if ctx.KubevirtCluster.Spec.ControlPlaneServiceTemplate.Spec.Type == "LoadBalancer" {
		lbip4, err := externalLoadBalancer.ExternalIP(ctx)
		if err != nil {
			conditions.Set(ctx.KubevirtCluster, metav1.Condition{
				Type:    infrav1.LoadBalancerAvailableCondition,
				Status:  metav1.ConditionFalse,
				Reason:  infrav1.LoadBalancerProvisioningFailedReason,
				Message: fmt.Sprintf("%v", err.Error()),
			})
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
			conditions.Set(ctx.KubevirtCluster, metav1.Condition{
				Type:    infrav1.LoadBalancerAvailableCondition,
				Status:  metav1.ConditionFalse,
				Reason:  infrav1.LoadBalancerProvisioningFailedReason,
				Message: fmt.Sprintf("%v", err.Error()),
			})
			return ctrl.Result{}, errors.Wrap(err, "failed to get ClusterIP for the load balancer")
		}
		ctx.KubevirtCluster.Spec.ControlPlaneEndpoint = infrav1.APIEndpoint{
			Host: lbip4,
			Port: 6443,
		}
	}

	conditions.Set(ctx.KubevirtCluster, metav1.Condition{
		Type:    infrav1.LoadBalancerAvailableCondition,
		Status:  metav1.ConditionTrue,
		Reason:  clusterv1.UpToDateReason,
		Message: "",
	})

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
	conditions.Set(ctx.KubevirtCluster, metav1.Condition{
		Type:    infrav1.LoadBalancerAvailableCondition,
		Status:  metav1.ConditionFalse,
		Reason:  clusterv1.DeletingReason,
		Message: "",
	})
	if err := ctx.PatchKubevirtCluster(patchHelper); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to patch KubevirtCluster")
	}
	for _, extraKind := range []schema.GroupVersionKind{
		schema.FromAPIVersionAndKind("/v1", "ConfigMapList"),
		schema.FromAPIVersionAndKind("/v1", "ServiceAccountList"),
		schema.FromAPIVersionAndKind("apps/v1", "DeploymentList"),
		schema.FromAPIVersionAndKind("rbac.authorization.k8s.io/v1", "RoleList"),
		schema.FromAPIVersionAndKind("rbac.authorization.k8s.io/v1", "RoleBindingList"),
	} {
		if err := r.deleteExtraGVK(ctx, extraKind); err != nil {
			return ctrl.Result{}, errors.Wrapf(err, "failed to delete extra %s", extraKind)
		}
	}

	// Cluster is deleted so remove the finalizer.
	controllerutil.RemoveFinalizer(ctx.KubevirtCluster, infrav1.ClusterFinalizer)

	return ctrl.Result{}, nil
}

// SetupWithManager will add watches for this controller.
func (r *KubevirtClusterReconciler) SetupWithManager(ctx gocontext.Context, mgr ctrl.Manager) error {
	clusterAPIVersions, err := crds.GetSupportedVersions(ctx, mgr.GetAPIReader(), clusterCRDName)
	if err != nil {
		return fmt.Errorf("unable to get CRD versions of %s: %w", clusterCRDName, err)
	}

	typedBuilder := ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.KubevirtCluster{}).
		WithEventFilter(predicates.ResourceNotPaused(r.Scheme(), r.Log)).
		WithEventFilter(predicates.ResourceIsNotExternallyManaged(r.Scheme(), r.Log))

	if slices.Contains(clusterAPIVersions, "v1beta2") {
		r.Log.Info("reconciling cluster-api Cluster v1beta2")
		r.GetOwnerCluster = util.GetOwnerCluster
		typedBuilder.Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(util.ClusterToInfrastructureMapFunc(
				ctx,
				infrav1.GroupVersion.WithKind("KubevirtCluster"),
				mgr.GetClient(),
				&infrav1.KubevirtCluster{},
			)),
			builder.WithPredicates(predicates.ClusterUnpaused(r.Scheme(), r.Log)),
		)
	} else if slices.Contains(clusterAPIVersions, "v1beta1") {
		r.Log.Info("reconciling cluster-api Cluster v1beta1")
		r.GetOwnerCluster = capiv1beta1.GetOwnerCluster
		typedBuilder.Watches(
			&clusterv1beta1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(capiv1beta1.MapV1beta1ClusterToKVKind(mgr.GetClient(), "KubevirtCluster")),
			builder.WithPredicates(predicates.ClusterUnpaused(r.Scheme(), r.Log)),
		)
	} else {
		return fmt.Errorf("unsupported cluster-api versions: %v", clusterAPIVersions)
	}

	return typedBuilder.Complete(r)
}

func (r *KubevirtClusterReconciler) deleteExtraGVK(ctx *context.ClusterContext, extraGVK schema.GroupVersionKind) error {
	if ctx.KubevirtCluster == nil {
		return nil
	}

	// List the Pods matching the PodTemplate Labels, but only their metadata
	var extraResourceMetaList metav1.PartialObjectMetadataList
	extraResourceMetaList.SetGroupVersionKind(extraGVK)
	extraResourceLabels := map[string]string{"cluster.x-k8s.io/cluster-name": ctx.Cluster.Name, "capk.cluster.x-k8s.io/template-kind": "extra-resource"}
	if err := r.APIReader.List(ctx, &extraResourceMetaList, client.InNamespace(ctx.Cluster.Namespace), client.MatchingLabels(extraResourceLabels)); err != nil {
		return errors.Wrap(err, "failed listing cluster extra object meta")
	}

	for _, extraResourceMeta := range extraResourceMetaList.Items {
		if err := r.Delete(ctx.Context, &extraResourceMeta, &client.DeleteOptions{}); err != nil {
			return errors.Wrap(err, "failed deleting cluster extra object meta")
		}
	}

	return nil
}

func (r *KubevirtClusterReconciler) filterOutNotFoundError(err error) error {
	if err == nil {
		return nil
	}
	var aggErr utilerrors.Aggregate
	if errors.As(err, &aggErr) {
		var errList []error
		for _, err := range aggErr.Errors() {
			if !apierrors.IsNotFound(err) {
				errList = append(errList, err)
			}
		}
		return utilerrors.NewAggregate(errList)
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
