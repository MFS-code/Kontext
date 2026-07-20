# Image URL to use all building/pushing image targets
IMG ?= kontext-operator:dev
ECHO_IMG ?= kontext-echo:dev
ANTHROPIC_IMG ?= kontext-runtime-anthropic:dev
REPORTER_IMG ?= kontext-reporter:dev
REFERENCE_IMG ?= kontext-reference:dev
STDOUT_FIXTURE_IMG ?= kontext-stdout-fixture:dev

# Generic image-build inputs. Convenience targets below select these per image.
DOCKERFILE ?= Dockerfile
CONTEXT ?= .
DIST_DIR ?= dist

# Get the currently used golang version
GO_VERSION ?= 1.26.5

# Pin govulncheck so CI and local runs share the same analyzer.
GOVULNCHECK_VERSION ?= v1.6.0

# CONTROLLER_TOOLS_VERSION defines the controller-gen version
CONTROLLER_TOOLS_VERSION ?= v0.17.2

# ENVTEST_K8S_VERSION refers to the Kubernetes version for envtest binaries
ENVTEST_K8S_VERSION = 1.32.0

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin

## Tool Binaries
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOVULNCHECK ?= $(LOCALBIN)/govulncheck

# Setting SHELL to bash allows bash commands to be executed by recipes.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd:allowDangerousTypes=true webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: verify
verify: manifests generate test-release-identity ## Check generated artifacts, release identity, and gofmt; fail on drift.
	@if ! git diff --quiet --exit-code -- \
		api/v1alpha1/zz_generated.deepcopy.go \
		config/crd/bases \
		config/rbac/role.yaml; then \
		echo "generated files are out of date; run 'make manifests generate' and commit the result" >&2; \
		git --no-pager diff -- api/v1alpha1/zz_generated.deepcopy.go config/crd/bases config/rbac/role.yaml; \
		exit 1; \
	fi
	@unformatted="$$(find . -name '*.go' -not -path './vendor/*' -not -path './bin/*' -exec gofmt -l {} +)"; \
	if [[ -n "$${unformatted}" ]]; then \
		echo "go files need formatting; run 'make fmt'" >&2; \
		echo "$${unformatted}"; \
		exit 1; \
	fi

.PHONY: test
test: manifests generate fmt vet envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" go test ./... -coverprofile cover.out

.PHONY: vulncheck
vulncheck: $(GOVULNCHECK) ## Scan for known Go vulnerabilities in reachable code.
	$(GOVULNCHECK) ./...

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: build-eval
build-eval: fmt vet ## Build the external evaluation runner.
	go build -o bin/kontext-eval ./cmd/kontext-eval

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

##@ Build

.PHONY: docker-build-image
docker-build-image: ## Build IMAGE from DOCKERFILE and CONTEXT.
	@test -n "$(IMAGE)" || { echo "IMAGE is required" >&2; exit 2; }
	docker build -f "$(DOCKERFILE)" -t "$(IMAGE)" "$(CONTEXT)"

.PHONY: docker-build
docker-build: ## Build docker image for the operator.
	$(MAKE) docker-build-image IMAGE="$(IMG)" DOCKERFILE="Dockerfile" CONTEXT="."

.PHONY: docker-build-echo
docker-build-echo: ## Build docker image for the echo runtime.
	$(MAKE) docker-build-image IMAGE="$(ECHO_IMG)" DOCKERFILE="runtimes/echo/Dockerfile" CONTEXT="runtimes/echo"

.PHONY: docker-build-anthropic
docker-build-anthropic: ## Build the unmaintained legacy Python example.
	$(MAKE) docker-build-image IMAGE="$(ANTHROPIC_IMG)" DOCKERFILE="runtimes/python-anthropic/Dockerfile" CONTEXT="runtimes/python-anthropic"

.PHONY: docker-build-reporter
docker-build-reporter: ## Build the reusable result reporter image.
	$(MAKE) docker-build-image IMAGE="$(REPORTER_IMG)" DOCKERFILE="runtimes/reporter/Dockerfile" CONTEXT="."

.PHONY: docker-build-reference
docker-build-reference: ## Build the model-agnostic reference runtime image.
	$(MAKE) docker-build-image IMAGE="$(REFERENCE_IMG)" DOCKERFILE="runtimes/reference/Dockerfile" CONTEXT="."

.PHONY: docker-build-stdout-fixture
docker-build-stdout-fixture: ## Build the generic image used by stdout-capture e2e.
	$(MAKE) docker-build-image IMAGE="$(STDOUT_FIXTURE_IMG)" DOCKERFILE="runtimes/stdout-fixture/Dockerfile" CONTEXT="runtimes/stdout-fixture"

.PHONY: docker-build-all
docker-build-all: docker-build docker-build-echo docker-build-reporter docker-build-reference ## Build operator and maintained runtime images.

.PHONY: kind-install
kind-install: docker-build docker-build-echo docker-build-reporter docker-build-reference docker-build-stdout-fixture ## Build kind images and install the operator into kind.
	./scripts/install-go-kind.sh

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	docker push ${IMG}

##@ Release

.PHONY: test-release-identity
test-release-identity: ## Verify release tooling uses the canonical repository and registry.
	./scripts/test-release-identity.sh

.PHONY: release-manifest
release-manifest: ## Render DIST_DIR/install.yaml from IMAGE_DIGESTS.
	@test -f "$(IMAGE_DIGESTS)" || { echo "IMAGE_DIGESTS is required" >&2; exit 2; }
	mkdir -p "$(DIST_DIR)"
	./scripts/render-release-manifest.sh "$(IMAGE_DIGESTS)" >"$(DIST_DIR)/install.yaml"

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	kubectl apply -f config/crd/bases

.PHONY: uninstall
uninstall: manifests ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config.
	kubectl delete --ignore-not-found=$(ignore-not-found) -f config/crd/bases

.PHONY: deploy
deploy: manifests ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	KONTEXT_OPERATOR_IMAGE="${IMG}" KONTEXT_REPORTER_IMAGE="${REPORTER_IMG}" ./scripts/deploy-go.sh

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config.
	kubectl delete --ignore-not-found=$(ignore-not-found) -k config/default

##@ Dependencies

$(LOCALBIN):
	mkdir -p $(LOCALBIN)

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest if necessary.
$(ENVTEST): $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.20

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: govulncheck-tool
govulncheck-tool: $(GOVULNCHECK) ## Download govulncheck locally if necessary.
$(GOVULNCHECK): $(LOCALBIN)
	$(call go-install-tool,$(GOVULNCHECK),golang.org/x/vuln/cmd/govulncheck,$(GOVULNCHECK_VERSION))

# go-install-tool will 'go install' any package with a custom version
define go-install-tool
@[ -f $(1) ] || { \
set -e ;\
GOBIN=$(LOCALBIN) go install $(2)@$(3) ;\
}
endef
