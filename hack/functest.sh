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
make e2e-test
