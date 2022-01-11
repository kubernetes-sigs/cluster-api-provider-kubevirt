#!/bin/bash

set -e -o pipefail

TOOLS_DIR=${TOOLS_DIR:-hack/tools}

CLUSTERCTL_PATH=${TOOLS_DIR}/bin/clusterctl
KUBECTL_PATH=${TOOLS_DIR}/bin/kubectl
TEST_WORKING_DIR=${TOOLS_DIR}/e2e-test-workingdir

if [ ! -f "${CLUSTERCTL_PATH}" ]; then
	echo >&2 "Downloading clusterctl ..."
	curl -L https://github.com/kubernetes-sigs/cluster-api/releases/download/v1.0.0/clusterctl-linux-amd64 -o ${CLUSTERCTL_PATH}
	chmod u+x ${CLUSTERCTL_PATH}
fi

if [ ! -f "${KUBECTL_PATH}" ]; then
	echo >&2 "Downloading kubectl ..."
	curl -L "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" -o ${KUBECTL_PATH}
	chmod u+x ${KUBECTL_PATH}
fi

rm -rf $TEST_WORKING_DIR
mkdir -p $TEST_WORKING_DIR
$BIN_DIR/e2e-tests --kubectl-path $KUBECTL_PATH --clusterctl-path $CLUSTERCTL_PATH --working-dir $TEST_WORKING_DIR
