#!/bin/bash

set -e -o pipefail

./kubevirtci up
./kubevirtci sync
./kubevirtci create-cluster
./kubevirtci kubectl wait --for=condition=ControlPlaneInitialized=true cluster/kvcluster --timeout=10m
./kubevirtci kubectl wait --for=condition=ControlPlaneReady=true cluster/kvcluster --timeout=10m
