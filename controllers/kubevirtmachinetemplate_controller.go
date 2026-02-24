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
	"reflect"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/cluster-api/util/patch"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
)

// KubevirtMachineTemplateReconciler reconciles a KubevirtMachineTemplate object.
type KubevirtMachineTemplateReconciler struct {
	client.Client
	Log logr.Logger
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kubevirtmachinetemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kubevirtmachinetemplates/status,verbs=get;update;patch

// Reconcile handles KubevirtMachineTemplate events and updates the status.capacity field
// based on the VM template's resource specifications.
func (r *KubevirtMachineTemplateReconciler) Reconcile(ctx gocontext.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("kubevirtmachinetemplate", req.NamespacedName)

	// Fetch the KubevirtMachineTemplate instance
	var machineTemplate infrav1.KubevirtMachineTemplate
	if err := r.Get(ctx, req.NamespacedName, &machineTemplate); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		log.Error(err, "unable to fetch KubevirtMachineTemplate")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	helper, err := patch.NewHelper(&machineTemplate, r.Client)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to init patch helper: %w", err)
	}

	// Extract capacity information from the VM template
	capacity := r.extractCapacity(machineTemplate)

	// Check if the status needs to be updated. Use our capacityEqual to correctly compare Quantity.
	if !reflect.DeepEqual(machineTemplate.Status.Capacity, capacity) {
		log.Info("Updating capacity status", "capacity", capacity)
		machineTemplate.Status.Capacity = capacity
		if err := helper.Patch(ctx, &machineTemplate); err != nil {
			if !apierrors.IsNotFound(err) {
				log.Error(err, "failed to patch machineTemplate")
				return ctrl.Result{}, err
			}
		}
	}

	return ctrl.Result{}, nil
}

// extractCapacity extracts capacity information from the VM template specification.
// The capacity is derived from:
// 1. spec.template.spec.virtualMachineTemplate.spec.template.spec.domain.cpu (cores * sockets * threads) (priority for CPU)
// 2. spec.template.spec.virtualMachineTemplate.spec.template.spec.domain.resources.requests (memory, cpu fallback)
// 3. spec.template.spec.virtualMachineTemplate.spec.template.spec.domain.memory.guest (if memory not in resources)
func (r *KubevirtMachineTemplateReconciler) extractCapacity(mt infrav1.KubevirtMachineTemplate) corev1.ResourceList {
	vmTemplate := mt.Spec.Template.Spec.VirtualMachineTemplate
	capacity := make(corev1.ResourceList)

	if cpu := r.extractCPUCapacity(vmTemplate); cpu != nil {
		capacity[corev1.ResourceCPU] = *cpu
	}

	if mem := r.extractMemoryCapacity(vmTemplate); mem != nil {
		capacity[corev1.ResourceMemory] = *mem
	}

	return capacity
}

func (r *KubevirtMachineTemplateReconciler) extractCPUCapacity(vmTemplate infrav1.VirtualMachineTemplateSpec) *resource.Quantity {
	if vmTemplate.Spec.Template == nil {
		return nil
	}
	domain := vmTemplate.Spec.Template.Spec.Domain

	// Extract CPU capacity
	// Priority: domain.cpu (cores * sockets * threads) > domain.resources.requests.cpu > domain.resources.limits.cpu
	if domain.CPU != nil {
		cores := uint32(1)
		if domain.CPU.Cores > 0 {
			cores = domain.CPU.Cores
		}
		sockets := uint32(1)
		if domain.CPU.Sockets > 0 {
			sockets = domain.CPU.Sockets
		}
		threads := uint32(1)
		if domain.CPU.Threads > 0 {
			threads = domain.CPU.Threads
		}
		vcpus := cores * sockets * threads
		return resource.NewQuantity(int64(vcpus), resource.DecimalSI)
	}

	if domain.Resources.Requests != nil {
		if cpu, ok := domain.Resources.Requests[corev1.ResourceCPU]; ok {
			return &cpu
		}
	}

	if domain.Resources.Limits != nil {
		if cpu, ok := domain.Resources.Limits[corev1.ResourceCPU]; ok {
			return &cpu
		}
	}

	return nil
}

func (r *KubevirtMachineTemplateReconciler) extractMemoryCapacity(vmTemplate infrav1.VirtualMachineTemplateSpec) *resource.Quantity {
	if vmTemplate.Spec.Template == nil {
		return nil
	}
	domain := vmTemplate.Spec.Template.Spec.Domain

	// Extract Memory capacity
	// Priority: domain.resources.requests.memory > domain.memory.guest > domain.resources.limits.memory
	if domain.Resources.Requests != nil {
		if mem, ok := domain.Resources.Requests[corev1.ResourceMemory]; ok {
			return &mem
		}
	}

	if domain.Memory != nil && domain.Memory.Guest != nil {
		return domain.Memory.Guest
	}

	if domain.Resources.Limits != nil {
		if mem, ok := domain.Resources.Limits[corev1.ResourceMemory]; ok {
			return &mem
		}
	}

	return nil
}

// SetupWithManager will add watches for this controller.
func (r *KubevirtMachineTemplateReconciler) SetupWithManager(ctx gocontext.Context, mgr ctrl.Manager, options controller.Options) error {
	log := ctrl.Log.WithName("controllers").WithName("KubevirtMachineTemplate")
	log.Info("Setting up KubevirtMachineTemplate controller")

	if err := ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.KubevirtMachineTemplate{}).
		WithOptions(options).
		Complete(r); err != nil {
		return fmt.Errorf("failed to set up KubevirtMachineTemplate controller: %w", err)
	}

	return nil
}
