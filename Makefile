# If you update this file, please follow:
# https://suva.sh/posts/well-documented-makefiles/

.DEFAULT_GOAL:=help

# binary versions
BIN_DIR ?= ./bin
BUILDER_GOLANG_VERSION ?= 1.24
GOLANGCI_VERSION ?= 2.5.0

GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

# image parameters
BUILD_ARGS = --build-arg BUILDER_GOLANG_VERSION=$(BUILDER_GOLANG_VERSION)
PLATFORMS ?= linux/amd64,linux/arm64
VERSION ?= "latest"
REGISTRY ?= "quay.io/spectrocloud-labs"
CLEANUP_IMG ?= "$(REGISTRY)/spectro-cleanup:$(VERSION)"

##@ Help Targets
help:  ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[0m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Test Targets
.PHONY: test
test: static ## Run tests
	@mkdir -p _build/cov
	go test ./... -coverprofile cover.out

##@ Dev Targets
build-cleanup: static  ## Builds cleanup binary. Output to './bin' directory.
	go build -o bin/spectro-cleanup main.go

##@ Static Analysis Targets
static: fmt lint vet
fmt: ## Run go fmt against code
	go fmt ./...
lint: golangci-lint ## Run golangci-lint
	$(GOLANGCI_LINT) run
vet: ## Run go vet against code
	go vet ./...

##@ Image Targets
docker: ## Builds and pushes multi-arch docker image
	docker buildx create --name multiarch --use || true
	docker buildx build --platform $(PLATFORMS) $(BUILD_ARGS) -t $(CLEANUP_IMG) --push -f ./Dockerfile .
	docker buildx rm multiarch
docker-rmi: ## Remove the local docker images
	docker rmi $(CLEANUP_IMG)

## Tools & binaries
golangci-lint:
	if ! test -f $(BIN_DIR)/golangci-lint-linux-amd64; then \
		curl -LOs https://github.com/golangci/golangci-lint/releases/download/v$(GOLANGCI_VERSION)/golangci-lint-$(GOLANGCI_VERSION)-linux-amd64.tar.gz; \
		tar -zxf golangci-lint-$(GOLANGCI_VERSION)-linux-amd64.tar.gz; \
		mv golangci-lint-$(GOLANGCI_VERSION)-*/golangci-lint $(BIN_DIR)/golangci-lint-linux-amd64; \
		chmod +x $(BIN_DIR)/golangci-lint-linux-amd64; \
		rm -rf ./golangci-lint-$(GOLANGCI_VERSION)-linux-amd64*; \
	fi
	if ! test -f $(BIN_DIR)/golangci-lint-$(GOOS)-$(GOARCH); then \
		curl -LOs https://github.com/golangci/golangci-lint/releases/download/v$(GOLANGCI_VERSION)/golangci-lint-$(GOLANGCI_VERSION)-$(GOOS)-$(GOARCH).tar.gz; \
		tar -zxf golangci-lint-$(GOLANGCI_VERSION)-$(GOOS)-$(GOARCH).tar.gz; \
		mv golangci-lint-$(GOLANGCI_VERSION)-*/golangci-lint $(BIN_DIR)/golangci-lint-$(GOOS)-$(GOARCH); \
		chmod +x $(BIN_DIR)/golangci-lint-$(GOOS)-$(GOARCH); \
		rm -rf ./golangci-lint-$(GOLANGCI_VERSION)-$(GOOS)-$(GOARCH)*; \
	fi
GOLANGCI_LINT=$(BIN_DIR)/golangci-lint-$(GOOS)-$(GOARCH)

##@ Proto Targets
proto-lint: ## Lint the proto files
	buf lint proto

proto-breaking: ## Check for breaking changes
	buf breaking proto --against ".git#subdir=proto"

proto-build: ## Build proto files
	buf build proto

proto-gen: proto-breaking proto-lint proto-build ## Generate code from proto files
	buf generate proto

proto-push: proto-breaking proto-lint proto-build ## Push module to the buf schema registry
	buf push proto
