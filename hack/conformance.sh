#!/bin/bash

set -e -o pipefail

echo "Starting kubevirtci cluster"
./kubevirtci up

echo "Building and installing capk manager container"
./kubevirtci sync

echo "Create cluster"
./kubevirtci create-cluster

echo "Install calico in tenant cluster"
./kubevirtci install-calico

echo "Running sonobuoy conformance e2e test suite"
make run-conformance
