# syntax=docker/dockerfile:1
ARG GO_VERSION=1.22

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /out/kata-vfio-pci-device-plugin \
    ./cmd/kata-vfio-pci-device-plugin

FROM gcr.io/distroless/static:latest
COPY --from=build /out/kata-vfio-pci-device-plugin /usr/local/bin/kata-vfio-pci-device-plugin
# Runs as root inside the pod: the kubelet device-plugin socket dir
# (/var/lib/kubelet/device-plugins/) is root-owned on the host, and
# we need to bind a UNIX socket inside it.
ENTRYPOINT ["/usr/local/bin/kata-vfio-pci-device-plugin"]
