# Frequently Asked Questions

The present document lists a couple of frequently asked questions from our users and adopters.

## How do I use x CNI with cluster?

There is a great chance your tenant cluster CNI will conflict with infrastructure's cluster CNI.
To avoid issues, you need first to make sure you are using VXLAN on your CNI, and a customized PORT. Below you can find a few examples:

### Calico

To use Calico CNI, you need to set `CALICO_IPV4POOL_IPIP` to `Never` and `CALICO_IPV4POOL_VXLAN` to `Always`. To customize your VXLAN port, you need to set `FELIX_VXLANPORT` to the customized port number.

### Cilium

To use Cilium CNI, make sure your `tunnel` is set to `vxlan`. To customize your VXLAN port, you need to set `tunnel-port` to the customized port number.

### Flannel

To use Flannel you will need to define your `Backend` manually in `net-conf.json` file, usually inside your `ConfigMap`. You must set `Type` to `vxlan` and `Port` to your customized port number, like the example below:
```
  net-conf.json: |
    {
      "Network": "10.243.0.0/16",
      "Backend": {
        "Type": "vxlan",
        "Port": 1234
      }
    }
```

## Whenever I reboot a VM in Kubevirt, it looses is IP address, how do I fix it?

Unfortunately anyone using cluster-api-provider-kubevirt to manage long-lived clusters that are not ephemeral will suffer from this. Basically there are 3 paths to workaround this issue for now:

* Use a CNI that provides stable IP addresses, like for example `kube-ovn` or `ovn-k`.
* Use a secondary network with Multus to attach this secondary network to the VirtualMachines. This will involve assigning static MAC addresses to the VirtualMachine through something like `kubemacpool` and map those MAC addresses to IP addresses on a DHCP server in that network.
* Use ephemeral VirtualMachines by defining `spec.template.spec.virtualMachineTemplate.spec.runStrategy` to `Once` in your `KubevirtMachineTemplate`.


## How do I use disk images that are not in a container registry?

You can use [Containerized Data Importer
](https://github.com/kubevirt/containerized-data-importer) as a solution to work with your disks on Kubevirt. The `DataVolume` definition would go inside `spec.template.spec.virtualMachineTemplate.spec.dataVolumeTemplates` inside your `KubevirtMachineTemplate`. An example to import an image from http would be:
```
  dataVolumeTemplates:
  - apiVersion: cdi.kubevirt.io/v1beta1
    kind: DataVolume
    metadata:
      annotations:
        cdi.kubevirt.io/storage.deleteAfterCompletion: "false"
      name: my-data-volume
      namespace: default
    spec:
      pvc:
        accessModes:
        - ReadWriteOnce
        resources:
          requests:
            storage: 40Gi
        storageClassName: hostpath-csi
      source:
        http:
          url: https://example.com/image.qcow2
```

After defining the `DataVolume` it needs to be added as a volume in your `KubevirtMachineTemplate` under `spec.template.spec.virtualMachineTemplate.spec.template.spec.volumes`. For the example provided this would look like:
```
    volumes:
    - dataVolume:
        name: my-data-volume
        name: disk1
```

## Is it possible to use a VirtualMachineClusterInstancetype on my cluster?

Yes, you can use either `VirtualMachineClusterInstancetype` or `VirtualMachineInstancetype`. To use it, you just need to set it under `spec.template.spec.virtualMachineTemplate.spec.instancetype` in your `KubevirtMachineTemplate`.
Assuming you have a `VirtualMachineClusterInstancetype` named `standard`, this is how the setting would look like:
```
  instancetype:
    kind: VirtualMachineClusterInstancetype
    name: standard
```

