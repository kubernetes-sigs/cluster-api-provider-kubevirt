#!/bin/bash

set -e -o pipefail

echo "Building e2e test suite"
make build-e2e-test

echo "Starting kubevirtci cluster"
./kubevirtci up

echo "Building and installing capk manager container"
./kubevirtci sync

echo "Running e2e test suite"
export KUBECONFIG=$(./kubevirtci kubeconfig)
export NODE_VM_IMAGE_TEMPLATE=quay.io/kubevirtci/fedora-kubeadm:35
export IMAGE_REPO=k8s.gcr.io
export TENANT_CLUSTER_KUBERNETES_VERSION=v1.21.0
export CRI_PATH=/var/run/crio/crio.sock
export ROOT_VOLUME_SIZE=23Gi
export STORAGE_CLASS_NAME=rook-ceph-block
make e2e-test
