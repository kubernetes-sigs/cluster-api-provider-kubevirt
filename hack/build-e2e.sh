#!/bin/bash

set -ex

mkdir -p $BIN_DIR
go test ./e2e/ -c -o "$BIN_DIR/e2e.test"
