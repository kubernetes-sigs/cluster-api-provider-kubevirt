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

package kubevirt

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/kind/pkg/cluster/constants"

	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
)

type CommandExecutor interface {
	ExecuteCommand(command string) (string, error)
}

// prefixDataVolumeTemplates adds a prefix to all DataVolumeTemplates and
// their corresponding disks/volume references within the vm. Appending a
// unique prefix allows each DataVolume to be unique per vm in a capi
// machine deployment or machine set
func prefixDataVolumeTemplates(vm *kubevirtv1.VirtualMachine, prefix string) *kubevirtv1.VirtualMachine {
	if len(vm.Spec.DataVolumeTemplates) == 0 {
		return vm
	}

	dvNameMap := map[string]string{}
	for i := range vm.Spec.DataVolumeTemplates {

		prefixedName := fmt.Sprintf("%s-%s", prefix, vm.Spec.DataVolumeTemplates[i].Name)
		dvNameMap[vm.Spec.DataVolumeTemplates[i].Name] = prefixedName

		vm.Spec.DataVolumeTemplates[i].Name = prefixedName
	}

	for i, volume := range vm.Spec.Template.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil {
			prefixedName, ok := dvNameMap[volume.PersistentVolumeClaim.ClaimName]
			if ok {
				vm.Spec.Template.Spec.Volumes[i].PersistentVolumeClaim.ClaimName = prefixedName
			}
		} else if volume.DataVolume != nil {
			prefixedName, ok := dvNameMap[volume.DataVolume.Name]
			if ok {
				vm.Spec.Template.Spec.Volumes[i].DataVolume.Name = prefixedName
			}
		}
	}

	return vm
}

// newVirtualMachineFromKubevirtMachine creates VirtualMachine instance.
func newVirtualMachineFromKubevirtMachine(ctx *context.MachineContext, namespace string) *kubevirtv1.VirtualMachine {
	vmiTemplate := buildVirtualMachineInstanceTemplate(ctx)

	virtualMachine := &kubevirtv1.VirtualMachine{
		Spec: *ctx.KubevirtMachine.Spec.VirtualMachineTemplate.Spec.DeepCopy(),
	}

	virtualMachine.Spec.Template = vmiTemplate

	virtualMachine.APIVersion = "kubevirt.io/v1"
	virtualMachine.Kind = "VirtualMachine"

	virtualMachine.ObjectMeta = metav1.ObjectMeta{
		Name:      ctx.KubevirtMachine.Name,
		Namespace: namespace,
		Labels:    map[string]string{},
	}

	if ctx.KubevirtMachine.Spec.VirtualMachineTemplate.ObjectMeta.Labels != nil {
		virtualMachine.Labels = mapCopy(ctx.KubevirtMachine.Spec.VirtualMachineTemplate.ObjectMeta.Labels)
	}

	if ctx.KubevirtMachine.Spec.VirtualMachineTemplate.ObjectMeta.Annotations != nil {
		virtualMachine.Annotations = mapCopy(ctx.KubevirtMachine.Spec.VirtualMachineTemplate.ObjectMeta.Annotations)
	}

	virtualMachine.Labels["kubevirt.io/vm"] = ctx.KubevirtMachine.Name
	virtualMachine.Labels["name"] = ctx.KubevirtMachine.Name
	virtualMachine.Labels["cluster.x-k8s.io/role"] = nodeRole(ctx)
	virtualMachine.Labels["cluster.x-k8s.io/cluster-name"] = ctx.Cluster.Name

	// make each datavolume unique by appending machine name as a prefix
	virtualMachine = prefixDataVolumeTemplates(virtualMachine, ctx.KubevirtMachine.Name)

	return virtualMachine
}

func mapCopy(src map[string]string) map[string]string {
	dst := map[string]string{}
	for k, v := range src {
		dst[k] = v

	}
	return dst
}

// buildVirtualMachineInstanceTemplate creates VirtualMachineInstanceTemplateSpec.
func buildVirtualMachineInstanceTemplate(ctx *context.MachineContext) *kubevirtv1.VirtualMachineInstanceTemplateSpec {
	template := &kubevirtv1.VirtualMachineInstanceTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      map[string]string{},
			Annotations: map[string]string{},
		},
	}

	if ctx.KubevirtMachine.Spec.VirtualMachineTemplate.Spec.Template.ObjectMeta.Labels != nil {
		template.ObjectMeta.Labels = mapCopy(ctx.KubevirtMachine.Spec.VirtualMachineTemplate.Spec.Template.ObjectMeta.Labels)
	}

	if ctx.KubevirtMachine.Spec.VirtualMachineTemplate.Spec.Template.ObjectMeta.Annotations != nil {
		template.ObjectMeta.Annotations = mapCopy(ctx.KubevirtMachine.Spec.VirtualMachineTemplate.Spec.Template.ObjectMeta.Annotations)
	}

	template.ObjectMeta.Labels["kubevirt.io/vm"] = ctx.KubevirtMachine.Name
	template.ObjectMeta.Labels["name"] = ctx.KubevirtMachine.Name
	template.ObjectMeta.Labels["cluster.x-k8s.io/role"] = nodeRole(ctx)
	template.ObjectMeta.Labels["cluster.x-k8s.io/cluster-name"] = ctx.Cluster.Name

	template.Spec = *ctx.KubevirtMachine.Spec.VirtualMachineTemplate.Spec.Template.Spec.DeepCopy()

	cloudInitVolumeName := "cloudinitvolume"
	cloudInitVolume := kubevirtv1.Volume{
		Name: cloudInitVolumeName,
		VolumeSource: kubevirtv1.VolumeSource{
			CloudInitConfigDrive: &kubevirtv1.CloudInitConfigDriveSource{
				UserDataSecretRef: &corev1.LocalObjectReference{
					Name: *ctx.Machine.Spec.Bootstrap.DataSecretName + "-userdata",
				},
			},
		},
	}
	template.Spec.Volumes = append(template.Spec.Volumes, cloudInitVolume)

	cloudInitDisk := kubevirtv1.Disk{
		Name: cloudInitVolumeName,
		DiskDevice: kubevirtv1.DiskDevice{
			Disk: &kubevirtv1.DiskTarget{
				Bus: "virtio",
			},
		},
	}
	template.Spec.Domain.Devices.Disks = append(template.Spec.Domain.Devices.Disks, cloudInitDisk)

	return template
}

// nodeRole returns the role of this node ("control-plane" or "worker").
func nodeRole(ctx *context.MachineContext) string {
	if util.IsControlPlaneMachine(ctx.Machine) {
		return constants.ControlPlaneNodeRoleValue
	}
	return constants.WorkerNodeRoleValue
}
