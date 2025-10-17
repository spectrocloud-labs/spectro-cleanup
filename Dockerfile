ARG BUILDER_GOLANG_VERSION
ARG FIPS_MODULE

FROM --platform=$BUILDPLATFORM tonistiigi/xx AS xx

FROM --platform=$BUILDPLATFORM us-docker.pkg.dev/palette-images/build-base-images/golang:${BUILDER_GOLANG_VERSION:-1.25}-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
ARG FIPS_MODULE

COPY --from=xx / /

LABEL org.opencontainers.image.source="https://github.com/spectrocloud-labs/spectro-cleanup"

WORKDIR /workspace

# Install git for go mod download and clang for xx
RUN apk add --no-cache git clang

# Install cross-compilation toolchain using xx
RUN xx-apk add --no-cache musl-dev gcc

COPY . .

# Build
RUN export GOOS=${TARGETOS} && \
    export GOARCH=${TARGETARCH} && \
    export TARGETPLATFORM=${TARGETOS}/${TARGETARCH} && \
    export CC=$(xx-clang --print-target-triple)-clang && \
    export CXX=$(xx-clang --print-target-triple)-clang++ && \
    if [ "${FIPS_MODULE}" = "boringcrypto" ]; then \
        go-build-fips.sh -a -o cleanup; \
        go tool nm cleanup | grep FIPS; \
        assert-fips.sh cleanup; \
    elif [ "${FIPS_MODULE}" = "go" ]; then \
        GOFIPS140=v1.0.0 xx-go build -a -o cleanup; \
    else \
        go-build-static.sh -a -o cleanup; \
        assert-static.sh cleanup; \
    fi; \
    scan-govulncheck.sh cleanup

# Finalize
FROM gcr.io/distroless/static:latest AS cleanup

WORKDIR /
COPY --from=builder /workspace/cleanup .

ENTRYPOINT ["/cleanup"]
