#!/bin/bash

set -e

ginkgo build e2e-tests/

mkdir -p $BIN_DIR
mv e2e-tests/e2e-tests.test $BIN_DIR/e2e-tests

