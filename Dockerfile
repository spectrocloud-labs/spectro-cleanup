ARG BUILDER_GOLANG_VERSION
# Build the spectro cleanup binary
FROM --platform=linux/amd64 gcr.io/spectro-images-public/golang:${BUILDER_GOLANG_VERSION}-alpine AS builder
ARG CRYPTO_LIB
ENV GOEXPERIMENT=${CRYPTO_LIB:+boringcrypto}

WORKDIR /workspace

# Copy the go module manifests & download dependencies
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

# Copy the go source
COPY . .

# Build
RUN if [ ${CRYPTO_LIB} ]; \
    then \
      go-build-fips.sh -a -o cleanup spectro-cleanup/main.go ;\
    else \
      go-build-static.sh -a -o cleanup spectro-cleanup/main.go ;\
    fi
RUN if [ "${CRYPTO_LIB}" ]; then assert-static.sh atop; fi
RUN if [ "${CRYPTO_LIB}" ]; then assert-fips.sh atop; fi
# Scan
RUN scan-govulncheck.sh cleanup

# Finalize
FROM gcr.io/distroless/static:latest AS cleanup

WORKDIR /
COPY --from=builder /workspace/cleanup .

ENTRYPOINT ["/cleanup"]
