# Copyright 2021 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# If you update this file, please follow:
# https://suva.sh/posts/well-documented-makefiles/

.DEFAULT_GOAL:=help
TARGET ?= target
TESTS = $(shell go list ./... | grep -vE "./(i|apply)tests")
OUTFILE = $(TARGET)/tests/unittest.out
XUNIT = $(TARGET)/tests/TEST-units.xml
# Use GOPROXY environment variable if set
GOPROXY := $(shell go env GOPROXY)
ifeq ($(GOPROXY),)
GOPROXY := https://proxy.golang.org
endif
export GOPROXY

# Active module mode, as we use go modules to manage dependencies
export GO111MODULE=on

# This option is for running docker manifest command
export DOCKER_CLI_EXPERIMENTAL := enabled

# Directories.
ROOT_DIR := $(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))
BIN_DIR := bin
TOOLS_DIR := hack/tools
TOOLS_BIN_DIR := $(TOOLS_DIR)/bin

# Set --output-base for conversion-gen if we are not within GOPATH
ifneq ($(abspath $(ROOT_DIR)),$(shell go env GOPATH)/src/sigs.k8s.io/cluster-api-provider-kubevirt)
	CONVERSION_GEN_OUTPUT_BASE := --output-base=$(ROOT_DIR)
endif

# Binaries.
CONTROLLER_GEN := $(abspath $(TOOLS_BIN_DIR)/controller-gen)
CONVERSION_GEN := $(abspath $(TOOLS_BIN_DIR)/conversion-gen)
GOTESTSUM := $(abspath $(TOOLS_BIN_DIR)/gotestsum)
KUSTOMIZE_IMAGE = k8s.gcr.io/kustomize/kustomize:v3.8.7
KUSTOMIZE ?= docker run $(KUSTOMIZE_IMAGE)

# Define Docker related variables. Releases should modify and double check these vars.
REGISTRY ?= 127.0.0.1:5000
IMAGE_NAME ?= capk-manager
CONTROLLER_IMG ?= $(REGISTRY)/$(IMAGE_NAME)
ARCH ?= amd64
ALL_ARCH = amd64 arm arm64

# TAG is set to GIT_TAG in GCB, a git-based tag of the form vYYYYMMDD-hash, e.g., v20210120-v0.3.10-308-gc61521971.
TAG ?= dev

# Allow overriding the imagePullPolicy
PULL_POLICY ?= Always

all: test manager

help:  ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

## --------------------------------------
## Testing
## --------------------------------------

ARTIFACTS ?= _artifacts

.PHONY: test
test: ## Run tests.
	go test -v `go list ./... | grep -Ev "e2e|clusterkubevirtadm"` $(TEST_ARGS)

.PHONY: test-verbose
test-verbose: ## Run tests with verbose settings.
	TEST_ARGS="$(TEST_ARGS) -v" $(MAKE) test

.PHONY: test-junit
test-junit: $(GOTESTSUM) ## Run tests with verbose setting and generate a junit report.
	mkdir -p $(ARTIFACTS)
	(go test -json ./... $(TEST_ARGS); echo $$? > $(ARTIFACTS)/junit.infra_docker.exitcode) | tee $(ARTIFACTS)/junit.infra_docker.stdout
	$(GOTESTSUM) --junitfile $(ARTIFACTS)/junit.infra_docker.xml --raw-command cat $(ARTIFACTS)/junit.infra_docker.stdout
	exit $$(cat $(ARTIFACTS)/junit.infra_docker.exitcode)


.PHONY: build-e2e-test
build-e2e-test: ## Builds the test binary
	BIN_DIR=$(BIN_DIR) ./hack/build-e2e.sh

.PHONY: e2e-test
e2e-test: build-e2e-test ## run e2e tests
	BIN_DIR=$(BIN_DIR) ./hack/run-e2e.sh

.PHONY: functest
functest:
	./hack/functest.sh

.PHONY: conformance
conformance:
	./hack/conformance.sh

.PHONY: run-conformance
run-conformance:
	./hack/run-conformance.sh

## --------------------------------------
## Binaries
## --------------------------------------

.PHONY: manager
manager: ## Build manager binary
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(BIN_DIR)/manager

$(CONTROLLER_GEN): $(TOOLS_DIR)/go.mod # Build controller-gen from tools folder.
	cd $(TOOLS_DIR); go build -tags=tools -o $(BIN_DIR)/controller-gen sigs.k8s.io/controller-tools/cmd/controller-gen

$(GOTESTSUM): $(TOOLS_DIR)/go.mod # Build gotestsum from tools folder.
	cd $(TOOLS_DIR); go build -tags=tools -o $(BIN_DIR)/gotestsum gotest.tools/gotestsum

$(CONVERSION_GEN): $(TOOLS_DIR)/go.mod
	cd $(TOOLS_DIR); go build -tags=tools -o $(BIN_DIR)/conversion-gen k8s.io/code-generator/cmd/conversion-gen

.PHONY: clusterkubevirtadm-test
clusterkubevirtadm-test:
	go test ./clusterkubevirtadm/...

.PHONY: clusterkubevirtadm-all
clusterkubevirtadm: clusterkubevirtadm-test clusterkubevirtadm-linux clusterkubevirtadm-macos clusterkubevirtadm-win

.PHONY: clusterkubevirtadm-linux
clusterkubevirtadm-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(BIN_DIR)/clusterkubevirtadm-linux-amd64 ./clusterkubevirtadm/

.PHONY: clusterkubevirtadm-macos
clusterkubevirtadm-macos:
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o $(BIN_DIR)/clusterkubevirtadm-darwin-amd64 ./clusterkubevirtadm/

.PHONY: clusterkubevirtadm-win
clusterkubevirtadm-win:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o $(BIN_DIR)/clusterkubevirtadm.exe ./clusterkubevirtadm/

controller-gen: $(CONTROLLER_GEN) ## Build a local copy of controller-gen.
conversion-gen: $(CONVERSION_GEN) ## Build a local copy of conversion-gen.
gotestsum: $(GOTESTSUM) ## Build a local copy of gotestsum.

## --------------------------------------
## Generate / Manifests
## --------------------------------------

.PHONY: check-gen
check-gen: generate
	hack/check-gen.sh

.PHONY: generate
generate: $(CONTROLLER_GEN) ## Generate code
	$(MAKE) generate-manifests
	$(MAKE) generate-go
	$(MAKE) generate-kccm-flavors

.PHONY: generate-go
generate-go: $(CONTROLLER_GEN) $(CONVERSION_GEN) ## Runs Go related generate targets
	$(CONTROLLER_GEN) \
		object:headerFile=hack/boilerplate.generatego.txt \
		paths=./api/...
	(IFS=','; for i in "./api/v1alpha1"; do find $$i -type f -name 'zz_generated.conversion*' -exec rm -f {} \;; done)
	$(CONVERSION_GEN) \
		--input-dirs=./api/v1alpha1 \
		--build-tag=ignore_autogenerated_capk_v1alpha1 \
		--extra-peer-dirs=sigs.k8s.io/cluster-api/api/v1beta1 \
		--output-file-base=zz_generated.conversion $(CONVERSION_GEN_OUTPUT_BASE) \
		--go-header-file=hack/boilerplate.generatego.txt

.PHONY: generate-manifests
generate-manifests: $(CONTROLLER_GEN) ## Generate manifests e.g. CRD, RBAC etc.
	$(CONTROLLER_GEN) \
		paths=./api/... \
		paths=./controllers/... \
		crd:crdVersions=v1 \
		rbac:roleName=manager-role \
		output:crd:dir=./config/crd/bases \
		output:webhook:dir=./config/webhook \
		webhook

.PHONY: generate-kccm-flavors
generate-kccm-flavors:
	./hack/kccm-flavor-gen.sh

.PHONY: modules
modules: ## Runs go mod to ensure modules are up to date.
	go mod tidy
	cd $(TOOLS_DIR); go mod tidy

## --------------------------------------
## Docker
## --------------------------------------

.PHONY: docker-pull-prerequisites
docker-pull-prerequisites:
	docker pull docker.io/docker/dockerfile:1.4
	docker pull docker.io/library/golang:1.18.2
	docker pull gcr.io/distroless/static:latest

.PHONY: docker-build
docker-build: docker-pull-prerequisites ## Build the docker image for controller-manager
	DOCKER_BUILDKIT=1 docker build --build-arg goproxy="$(GOPROXY)" --build-arg ARCH=$(ARCH) . -t $(CONTROLLER_IMG)-$(ARCH):$(TAG) --file Dockerfile
	MANIFEST_IMG=$(CONTROLLER_IMG)-$(ARCH) MANIFEST_TAG=$(TAG) $(MAKE) set-manifest-image
	$(MAKE) set-manifest-pull-policy

.PHONY: docker-push
docker-push: ## Push the docker image
	docker push $(CONTROLLER_IMG)-$(ARCH):$(TAG)

## --------------------------------------
## Docker — All ARCH
## --------------------------------------

.PHONY: docker-build-all ## Build all the architecture docker images
docker-build-all: $(addprefix docker-build-,$(ALL_ARCH))

docker-build-%:
	$(MAKE) ARCH=$* docker-build

.PHONY: docker-push-all ## Push all the architecture docker images
docker-push-all: $(addprefix docker-push-,$(ALL_ARCH))
	$(MAKE) docker-push-manifest

docker-push-%:
	$(MAKE) ARCH=$* docker-push

.PHONY: docker-push-manifest
docker-push-manifest: ## Push the fat manifest docker image.
	## Minimum docker version 18.06.0 is required for creating and pushing manifest images.
	docker manifest create --amend $(CONTROLLER_IMG):$(TAG) $(shell echo $(ALL_ARCH) | sed -e "s~[^ ]*~$(CONTROLLER_IMG)\-&:$(TAG)~g")
	@for arch in $(ALL_ARCH); do docker manifest annotate --arch $${arch} ${CONTROLLER_IMG}:${TAG} ${CONTROLLER_IMG}-$${arch}:${TAG}; done
	docker manifest push --purge $(CONTROLLER_IMG):$(TAG)
	MANIFEST_IMG=$(CONTROLLER_IMG) MANIFEST_TAG=$(TAG) $(MAKE) set-manifest-image
	$(MAKE) set-manifest-pull-policy

.PHONY: set-manifest-image
set-manifest-image:
	$(info Updating kustomize image patch file for manager resource)
ifeq ($(shell uname -s), Darwin)
	sed -i '' -e 's@image: .*@image: '"${MANIFEST_IMG}:$(MANIFEST_TAG)"'@' ./config/default/manager_image_patch.yaml
else
	sed -i -e 's@image: .*@image: '"${MANIFEST_IMG}:$(MANIFEST_TAG)"'@' ./config/default/manager_image_patch.yaml
endif

.PHONY: set-manifest-pull-policy
set-manifest-pull-policy:
	$(info Updating kustomize pull policy file for manager resource)
ifeq ($(shell uname -s), Darwin)
	sed -i '' -e 's@imagePullPolicy: .*@imagePullPolicy: '"$(PULL_POLICY)"'@' ./config/default/manager_pull_policy.yaml
else
	sed -i -e 's@imagePullPolicy: .*@imagePullPolicy: '"$(PULL_POLICY)"'@' ./config/default/manager_pull_policy.yaml
endif

## --------------------------------------
## Deployment
## --------------------------------------
##> infrastructure-components.yaml
set_controller_image:
	echo setting controller image to be ${CONTROLLER_IMG}:${TAG}
	cd config/default && kustomize edit set image controller=${CONTROLLER_IMG}:${TAG} 

create-infrastructure-components: generate-manifests set_controller_image
	kustomize build config/default > infrastructure-components.yaml


## --------------------------------------
## Cleanup / Verification
## --------------------------------------

.PHONY: clean
clean: ## Remove all generated files
	$(MAKE) clean-bin
	$(MAKE) clean-test

.PHONY: clean-bin
clean-bin: ## Remove all generated binaries
	rm -rf $(BIN_DIR)
	rm -rf $(TOOLS_BIN_DIR)

.PHONY: clean-release
clean-release: ## Remove the release folder
	rm -rf $(RELEASE_DIR)

.PHONY: verify
verify:
	./hack/verify-all.sh
	$(MAKE) verify-modules
	$(MAKE) verify-gen

.PHONY: verify-modules
verify-modules: modules
	@if !(git diff --quiet HEAD -- go.sum go.mod hack/tools/go.mod hack/tools/go.sum); then \
		git diff; \
		echo "go module files are out of date"; exit 1; \
	fi

.PHONY: verify-gen
verify-gen: generate
	@if !(git diff --quiet HEAD); then \
		git diff; \
		echo "generated files are out of date, run make generate"; exit 1; \
	fi

.PHONY: cluster-up
cluster-up:
	./kubevirtci up

.PHONY: cluster-down
cluster-down:
	./kubevirtci down

.PHONY: cluster-sync
cluster-sync:
	./kubevirtci sync

.PHONY: goimports
goimports:
	go install golang.org/x/tools/cmd/goimports@latest
	goimports -w -local="sigs.k8s.io/cluster-api-provider-kubevirt"  $(shell find . -type f -name '*.go' ! -path "*/vendor/*" ! -path "./_kubevirtci/*" ! -path "*zz_generated*" )

.PHONY: linter
linter:
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	# todo remove the exclude parameter when issue #85 is resolved
	golangci-lint run --exclude SA1019

.PHONY: sanity
sanity: linter goimports test
