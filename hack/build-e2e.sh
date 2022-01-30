#!/bin/bash

set -e

GOBIN="${GOBIN:-$GOPATH/bin}"
GINKGO=$GOBIN/ginkgo

if ! [ -x "$GINKGO" ]; then
	echo "Retrieving ginkgo build dependencies"
	go get github.com/onsi/ginkgo/ginkgo
else
	echo "GINKO binary found at $GINKGO"
fi

$GINKGO build e2e-tests/

mkdir -p $BIN_DIR
mv e2e-tests/e2e-tests.test $BIN_DIR/e2e-tests

