# Configure PCI passthrough

The present document shows the steps to passthrough the PCI devices of the host to worker nodes created by CAPK (Cluster API KubeVirt Provider).

## Configure the host

To enable passthrough PCI devices from the host to KubeVirt VM, enable the following features on the host:

- Enable IOMMU
- Bind target PCI devices to the `vfio-pci` driver

See [KubeVirt User-Guide](https://kubevirt.io/user-guide/virtual_machines/host-devices/#host-devices-assignment) for details.

### Find target PCI devices

First, you should find the information on the target PCI device on the host. All devices connected to the PCI bus of the host can be listed using `lspci`.

In this example, NVIDIA GPU with the PCI address `10de:20f1` is the target PCI device. `10de` and `20f1` correspond to `VendorID` and `ProductID`, respectively.

```bash
# lspci -Dnn
    ：
0000:06:00.0 3D controller [0302]: NVIDIA Corporation Device [10de:20f1] (rev a1)
    ：
```

### Enable IOMMU

To enable IOMMU, a host should be booted with an additional kernel parameter, `intel_iommu=on` for Intel and `amd_iommu=on` for AMD.

See [KubeVirt User-Guide](https://kubevirt.io/user-guide/virtual_machines/host-devices/#host-preparation-for-pci-passthrough) for details.

### Bind target PCI devices to `vfio-pci` driver

To allow the target PCI device to be handled by KubeVirt, it should be bound to the `vfio-pci` driver.

```bash
echo 0000:06:00.0 > /sys/bus/pci/drivers/nvidia/unbind
echo "vfio-pci" > /sys/bus/pci/devices/0000\:06\:00.0/driver_override
echo 0000:06:00.0 > /sys/bus/pci/drivers/vfio-pci/bind
```

See [KubeVirt User-Guide](https://kubevirt.io/user-guide/virtual_machines/host-devices/#host-preparation-for-pci-passthrough) for details.

:bulb: To make the configuration persistent after reboot, you can add `vfio-pci.ids=10de:20f1` to the kernel parameters.

## Configure KubeVirt

Register the target PCI device to KubeVirt CR (Custom Resource), see [KubeVirt User-Guide](https://kubevirt.io/user-guide/virtual_machines/host-devices/#listing-permitted-devices) for details.

Add the following configuration under `KubeVirt` CR `spec.configuration`.

In this example, the PCI device with the address `10de:20f1` is registered with the resource name `nvidia.com/gpu_dev1`.

```yaml
    permittedHostDevices:
      pciHostDevices:
      - pciVendorSelector: "10DE:20F1"
        resourceName: "nvidia.com/gpu_dev1"
    developerConfiguration:
      featureGates: ["GPU","HostDevices"]
```

An example of KubeVirt CR declaration:

``` yaml
---
apiVersion: kubevirt.io/v1
kind: KubeVirt
metadata:
  name: kubevirt
  namespace: kubevirt
spec:
  certificateRotateStrategy: {}
  configuration:
    permittedHostDevices:
      pciHostDevices:
      - pciVendorSelector: "10DE:20F1"
        resourceName: "nvidia.com/dev1"
    developerConfiguration:
      featureGates: ["GPU","HostDevices"]
  customizeComponents: {}
  imagePullPolicy: IfNotPresent
  infra:
  workloadUpdateStrategy: {}
```

### Configure CAPK

Add the definition of the target PCI device to `KubeVirtMachineTemplate` CR in the manifest generated by `clusterctl generate cluster`.

Add `gpus.deviceName` and `gpus.name` under `spec.template.spec.virtualMachineTemplate.spec.template.spec.domain.devices`

:bulb: You must find multiple `KubevirtMachineTemplate` CRs in the manifest. The one with the name `${CLUSTER_NAME}-control-plane` corresponds to the control plane and the one with the name `{cluster_name}-md-0` corresponds to the worker node, respectively.


An example of `KubevirtMachineTemplate` CR declaration:

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: KubevirtMachineTemplate
metadata:
  name: kubevirt-cl-md-0
  namespace: default
spec:
  template:
    spec:
      virtualMachineTemplate:
        metadata:
          namespace: default
        spec:
          runStrategy: Always
          template:
            spec:
              domain:
                cpu:
                  cores: 2
                devices:
                  disks:
                  - disk:
                      bus: virtio
                    name: containervolume
                  # --- PCI-passthrough device settings
                  gpus:
                  - deviceName: nvidia.com/dev1
                    name: gpu1
                  # ---
                memory:
                  guest: 4Gi
              evictionStrategy: External
              volumes:
              - containerDisk:
                  image: quay.io/capk/ubuntu-2004-container-disk:v1.22.0
                name: containervolume
```

From the above configuration, the GPU device (`10de:20f1`) attached to the host will appear on worker nodes created by CAPK.
