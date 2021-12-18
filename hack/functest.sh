#!/bin/bash

set -e -o pipefail

./kubevirtci up
./kubevirtci sync
./kubevirtci create-cluster
./kubevirtci kubectl wait --for=condition=ControlPlaneInitialized=true cluster/kvcluster --timeout=10m
./kubevirtci kubectl wait --for=condition=ControlPlaneReady=true cluster/kvcluster --timeout=10m

CONTROL_PLANE_MACHINE=$(./kubevirtci kubectl get kubevirtmachine | grep kvcluster-control-plane | awk '{ print $1 }')
./kubevirtci kubectl wait --for=condition=BootstrapExecSucceeded=true kubevirtmachine/${CONTROL_PLANE_MACHINE} --timeout=1m

