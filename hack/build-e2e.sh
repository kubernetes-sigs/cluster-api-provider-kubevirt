#!/bin/bash

set -ex

mkdir -p "${BIN_DIR}"
cd e2e/
go test -c -o "${BIN_DIR}/e2e.test" .
