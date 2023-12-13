# If you update this file, please follow:
# https://suva.sh/posts/well-documented-makefiles/

.DEFAULT_GOAL:=help

# Image URL to use all building/pushing image targets
CLEANUP_IMG ?= "gcr.io/spectro-common-dev/${USER}/spectro-cleanup:latest"

# binary versions
BIN_DIR ?= ./bin
FIPS_ENABLE ?= ""
BUILDER_GOLANG_VERSION ?= 1.21
GOLANGCI_VERSION ?= 1.55.2

GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

BUILD_ARGS = --build-arg CRYPTO_LIB=${FIPS_ENABLE} --build-arg BUILDER_GOLANG_VERSION=${BUILDER_GOLANG_VERSION}

##@ Help Targets
help:  ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[0m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Test Targets
.PHONY: test
test: static ## Run tests
	@mkdir -p _build/cov
	go test -covermode=atomic -coverpkg=./... -coverprofile _build/cov/coverage.out ./... -timeout 120m

##@ Dev Targets
build-cleanup: static  ## Builds cleanup binary. Output to './bin' directory.
	go build -o bin/spectro-cleanup main.go

##@ Static Analysis Targets
static: fmt lint vet
fmt: ## Run go fmt against code
	go fmt ./...
lint: golangci-lint ## Run golangci-lint
	$(GOLANGCI_LINT) run --verbose
vet: ## Run go vet against code
	go vet ./...

##@ Image Targets
docker: docker-build-cleanup docker-push ## Tags docker image and also pushes it to container registry
docker-build-cleanup: ## Builds docker image for Spectro Cleanup
	docker build . -t ${CLEANUP_IMG} ${BUILD_ARGS} -f ./Dockerfile
docker-push: ## Pushes docker images to container registry
	docker push ${CLEANUP_IMG}
docker-rmi: ## Remove the local docker images
	docker rmi ${CLEANUP_IMG}

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
