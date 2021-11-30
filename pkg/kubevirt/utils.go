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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/context"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/kind/pkg/cluster/constants"
)

const (
	clusterLabelKey  = "io.x-k8s.capk.cluster"
	nodeRoleLabelKey = "io.x-k8s.capk.role"
)

type CommandExecutor interface {
	ExecuteCommand(command string) (string, error)
}

// newVirtualMachineFromKubevirtMachine creates VirtualMachine instance.
func newVirtualMachineFromKubevirtMachine(ctx *context.MachineContext) *kubevirtv1.VirtualMachine {
	runAlways := kubevirtv1.RunStrategyAlways
	vmiTemplate := buildVirtualMachineInstanceTemplate(ctx)

	virtualMachine := &kubevirtv1.VirtualMachine{
		Spec: kubevirtv1.VirtualMachineSpec{
			RunStrategy: &runAlways,
			Template:    vmiTemplate,
		},
	}

	virtualMachine.APIVersion = "kubevirt.io/v1"
	virtualMachine.Kind = "VirtualMachine"

	virtualMachine.ObjectMeta = metav1.ObjectMeta{
		Name:      ctx.KubevirtMachine.Name,
		Namespace: ctx.KubevirtMachine.Namespace,
		Labels: map[string]string{
			"kubevirt.io/vm": ctx.KubevirtMachine.Name,
			clusterLabelKey:  ctx.KubevirtCluster.Name,
			nodeRoleLabelKey: nodeRole(ctx),
		},
	}

	return virtualMachine
}

// buildVirtualMachineInstanceTemplate creates VirtualMachineInstanceTemplateSpec.
func buildVirtualMachineInstanceTemplate(ctx *context.MachineContext) *kubevirtv1.VirtualMachineInstanceTemplateSpec {
	template := &kubevirtv1.VirtualMachineInstanceTemplateSpec{}

	template.ObjectMeta = metav1.ObjectMeta{
		Labels: map[string]string{
			"kubevirt.io/vm":        ctx.KubevirtMachine.Name,
			"name":                  ctx.KubevirtMachine.Name,
			"cluster.x-k8s.io/role": nodeRole(ctx),
		},
	}

	template.Spec = ctx.KubevirtMachine.Spec.VMSpec

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
