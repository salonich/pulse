SHELL := /bin/bash
GOPATH ?= $(shell go env GOPATH)
CONTROLLER_GEN := $(GOPATH)/bin/controller-gen
IMG_OPERATOR  ?= ghcr.io/velorai/pulse-operator:latest
IMG_PROXY     ?= ghcr.io/velorai/pulse-proxy:latest
IMG_COLLECTOR ?= ghcr.io/velorai/pulse-collector:latest

.PHONY: all build test generate manifests lint vet fmt docker-build docker-push help

all: generate fmt vet build test ## Run generate, fmt, vet, build, and test.

## ─── Go ─────────────────────────────────────────────────────────────────────

build: ## Build all binaries.
	go build ./cmd/operator
	go build ./cmd/proxy
	go build ./cmd/collector

test: ## Run unit tests.
	go test -race -count=1 ./...

fmt: ## Run gofmt.
	gofmt -w .

vet: ## Run go vet.
	go vet ./...

lint: ## Run golangci-lint (must be installed separately).
	golangci-lint run ./...

## ─── Code generation ─────────────────────────────────────────────────────────

generate: $(CONTROLLER_GEN) ## Run controller-gen to regenerate DeepCopy methods.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/..."

manifests: $(CONTROLLER_GEN) ## Generate CRD YAML into config/crd/bases/.
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:artifacts:config=config/crd/bases

$(CONTROLLER_GEN):
	go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.16.5

## ─── Docker ─────────────────────────────────────────────────────────────────

docker-build: ## Build all Docker images.
	docker build -f Dockerfile.operator  -t $(IMG_OPERATOR)  .
	docker build -f Dockerfile.proxy     -t $(IMG_PROXY)     .
	docker build -f Dockerfile.collector -t $(IMG_COLLECTOR) .

docker-push: ## Push all Docker images.
	docker push $(IMG_OPERATOR)
	docker push $(IMG_PROXY)
	docker push $(IMG_COLLECTOR)

## ─── Local dev ───────────────────────────────────────────────────────────────

dev: manifests generate ## Regenerate manifests and code for local development.

install: manifests ## Apply CRDs to the cluster in KUBECONFIG.
	kubectl apply -f config/crd/bases/

uninstall: ## Remove CRDs from the cluster.
	kubectl delete -f config/crd/bases/ --ignore-not-found

## ─── Misc ────────────────────────────────────────────────────────────────────

tidy: ## Run go mod tidy.
	go mod tidy

help: ## Print this help.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'
