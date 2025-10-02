ARG BUILDER_GOLANG_VERSION
FROM --platform=linux/amd64 gcr.io/spectro-images-public/golang:${BUILDER_GOLANG_VERSION}-alpine AS scanner

FROM --platform=linux/amd64 golang:1.25.1-alpine3.22@sha256:b6ed3fd0452c0e9bcdef5597f29cc1418f61672e9d3a2f55bf02e7222c014abd AS builder

COPY --from=scanner /usr/local/bin/scan-govulncheck.sh /usr/local/bin/scan-govulncheck.sh

RUN apk add --no-cache bash
RUN go install golang.org/x/vuln/cmd/govulncheck@latest

WORKDIR /workspace

# Copy the go module manifests & download dependencies
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

# Copy the go source
COPY . .

# Build and scan
RUN CGO_ENABLED=0 GOFIPS140=v1.0.0 go build -a -o cleanup -v main.go

# Scan
RUN bash /usr/local/bin/scan-govulncheck.sh cleanup

# Finalize
FROM gcr.io/distroless/static:latest AS cleanup

WORKDIR /
COPY --from=builder /workspace/cleanup .

ENTRYPOINT ["/cleanup"]
