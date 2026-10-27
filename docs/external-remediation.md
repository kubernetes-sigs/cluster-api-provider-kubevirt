# External remediation: restart instead of replace

By default, when a Cluster API `MachineHealthCheck` marks a Machine unhealthy,
the Machine is deleted and a replacement is created — a full node rebuild
(image pull, kubeadm join, addon rollout). For faults that a reboot fixes,
CAPK offers a cheaper first step through Cluster API's
[external remediation](https://cluster-api.sigs.k8s.io/tasks/automated-machine-management/healthchecking)
mechanism: restart the VirtualMachineInstance backing the Machine, and only
fall back to replacement if that does not help.

## How it works

1. A `MachineHealthCheck` with `spec.remediation.templateRef` pointing at a
   `KubevirtRemediationTemplate` creates a `KubevirtRemediation` object
   (named after the Machine, owned by it) instead of deleting the Machine.
2. The KubevirtRemediation controller deletes the Machine's
   VirtualMachineInstance. Because the VMs of the CAPK cluster templates run
   with `runStrategy: Always`, the KubeVirt VM controller immediately
   recreates the VMI — same VirtualMachine, same disks, same node name. This
   is the API-level equivalent of a power cycle.
3. If the node passes its health check again, the MachineHealthCheck
   controller deletes the `KubevirtRemediation` object and nothing else
   happens.
4. If the Machine is still unhealthy after `timeoutSeconds`, the controller
   restarts the VMI again, up to `retryLimit` times.
5. When the retry budget is exhausted — or a restart cannot bring the node
   back at all (its `runStrategy` is not `Always`, or its disks do not persist
   across a restart, see [Requirements](#requirements-and-caveats)) — the
   controller sets the `OwnerRemediated` condition to `False` on the Machine.
   The MachineSet controller then deletes and replaces the Machine, which is
   exactly what would have happened without external remediation.

## Enabling the controller

The KubevirtRemediation controller is opt-in. Install the
`KubevirtRemediation` and `KubevirtRemediationTemplate` CRDs first, then start
the CAPK manager with `--enable-remediation=true`, i.e. add the flag to the
args of the `manager` container in the `capk-controller-manager` Deployment
(`config/manager/manager.yaml` when building from source):

```yaml
args:
  - "--leader-elect"
  - "--feature-gates=MachinePool=false"
  - "--enable-remediation=true"
```

The order matters for installations that manage CRDs separately from the
controller image: a manager that watches a kind whose CRD is missing fails its
cache sync and stops, taking the other CAPK controllers down with it. With the
flag unset (the default) the manager does not watch the remediation kinds and
runs fine without the CRDs.

## Usage

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: KubevirtRemediationTemplate
metadata:
  name: worker-restart
  namespace: default
spec:
  template:
    spec:
      strategy:
        type: Reboot
        retryLimit: 2
        timeoutSeconds: 300
---
apiVersion: cluster.x-k8s.io/v1beta2
kind: MachineHealthCheck
metadata:
  name: worker-healthcheck
  namespace: default
spec:
  clusterName: my-cluster
  selector:
    matchLabels:
      cluster.x-k8s.io/deployment-name: my-workers
  checks:
    unhealthyNodeConditions:
      - type: Ready
        status: "False"
        timeoutSeconds: 300
      - type: Ready
        status: Unknown
        timeoutSeconds: 300
  remediation:
    templateRef:
      apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
      kind: KubevirtRemediationTemplate
      name: worker-restart
```

## Requirements and caveats

- The VirtualMachine must use `runStrategy: Always` (as all templates in
  `templates/` do). Under any other run strategy a deleted VMI is not
  recreated, so the controller refuses to restart and immediately hands the
  Machine back to Cluster API.
- The node's state must live on persistent storage (`dataVolume` /
  `persistentVolumeClaim`, as in `templates/cluster-template-persistent-storage.yaml`).
  Recreating the VMI discards the writable layer of a `containerDisk` and the
  overlay of an `ephemeral` volume: the node would boot from a pristine image
  without its kubelet credentials (and, on control plane nodes, without its
  local etcd data), and the bootstrap token it would need to join again has
  usually expired. If the VirtualMachine has any `containerDisk` or
  `ephemeral` volume, the controller does not restart it and immediately
  hands the Machine back to Cluster API. Note that most templates shipped in
  `templates/` use `containerDisk`; for those, external remediation degrades
  to plain replacement. `emptyDisk` and `hostDisk` volumes are not inspected:
  an `emptyDisk` is scratch space that is expected to start empty, and must
  not hold node state.
- Pausing is honored. While `Cluster.spec.paused` is set, or the Machine or
  the KubevirtRemediation carries the `cluster.x-k8s.io/paused` annotation,
  the controller neither restarts the VMI nor hands the Machine back, even if
  a retry was already pending. The MachineHealthCheck controller is paused
  for the same time, so its verdict is stale when the pause is lifted; the
  controller therefore stays idle for one more minute after the `Paused`
  conditions of the Cluster and the Machine turn `False`, which gives the
  MachineHealthCheck time to delete the request of a node that recovered in
  the meantime. The `cluster.x-k8s.io/paused` annotation on the Cluster object
  itself is not a pause in this sense: it stops neither the Machines nor the
  MachineHealthChecks, and it does not stop remediation either.
- Once the boot window of a restart has expired, the controller checks the
  Machine's `HealthCheckSucceeded` condition before retrying, and does nothing
  while it is `True`: the MachineHealthCheck has seen the node recover and is
  about to delete the remediation object. For the same reason a Machine is
  never handed back while the condition is `True`; its owner would not replace
  it anyway.
- The restart is a shutdown of the guest, not a drain: the node is neither
  cordoned nor drained first, just as when a node fails. That is what an
  unreachable node needs, and worth knowing when the health check reacts to a
  fault on a node that still runs workloads.
- Only failed preconditions (run strategy, non-persistent volumes, a missing
  VirtualMachine) hand the Machine back. Transient errors, such as an
  unreachable infrastructure cluster, are retried and never cause a
  replacement on their own.
- The controller is built for requests created by the MachineHealthCheck
  controller. Those are owned by the Machine (owner reference), named after
  it, and carry the `cluster.x-k8s.io/cluster-name` label; the controller
  relies on the name and the label to resume after a pause. Anything else
  that creates a `KubevirtRemediation` has to do the same, and should be aware
  of how Cluster API treats such a request:
  - A MachineHealthCheck remediating through a `KubevirtRemediationTemplate`
    treats the request as its own: it deletes it, whoever created it, as soon
    as it considers the Machine healthy, and while the request exists it
    neither creates one itself nor, as long as it is allowed to remediate,
    records `HealthCheckSucceeded=False`, so normally nothing escalates until
    the request is gone. A MachineHealthCheck without a remediation template
    ignores the request and has the Machine replaced as usual. Without a
    MachineHealthCheck deleting it, the creator has to delete the request.
  - The first restart is always issued. Retries and handing the Machine back
    only happen while `HealthCheckSucceeded` is not `True`, and the owner only
    replaces a Machine whose `HealthCheckSucceeded` is `False`, a verdict only
    a MachineHealthCheck gives.
- `timeoutSeconds` only has to outlast a normal boot of the node: the Machine
  is considered healthy again as soon as the MachineHealthCheck deletes the
  remediation object.
- Control plane Machines are remediated by their owner (for example
  KubeadmControlPlane), which applies its own safety rules on top.
