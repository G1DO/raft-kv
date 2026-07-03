# syntax=docker/dockerfile:1

# ---- build stage ----------------------------------------------------------
# Pinned to the toolchain in go.mod (go 1.25.11). Debian-based; used only to
# compile — nothing from it ships in the final image. Kept current with Go's
# security patch releases: the stdlib version is baked into the binary, so a
# stale toolchain is exactly what the Trivy gate (.github/workflows/trivy.yml)
# flags.
FROM golang:1.25.11 AS build

WORKDIR /src

# Module files first so the dependency download layer caches independently of
# source edits (deps: the OpenTelemetry SDK for trace export, ADR-007 — the
# consensus core itself is stdlib-only). Context is trimmed by .dockerignore.
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal

# BuildKit populates TARGETOS/TARGETARCH from the build platform (or from
# --platform), so the same Dockerfile cross-compiles without edits.
ARG TARGETOS
ARG TARGETARCH

# CGO off + stdlib-only => a fully static binary that runs on distroless/static.
# -trimpath drops local build paths; -s -w strip symbol and DWARF tables.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/raft-kv ./cmd/server

# Pre-create the data dir here so we can hand it to the (shell-less) final
# image already owned by the non-root uid. WAL + snapshots live here; in
# Kubernetes a per-pod PVC is mounted over this path.
RUN mkdir -p /data

# ---- runtime stage --------------------------------------------------------
# distroless/static: no shell, no package manager, no libc — only CA certs,
# /etc/passwd and tzdata. The :nonroot tag runs as uid/gid 65532.
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="raft-kv" \
      org.opencontainers.image.description="Distributed key-value store on a from-scratch Raft implementation" \
      org.opencontainers.image.source="https://github.com/G1DO/raft-kv"

COPY --from=build /out/raft-kv /raft-kv
COPY --from=build --chown=65532:65532 /data /data

# distroless :nonroot already sets this; stated explicitly so the image
# satisfies a runAsNonRoot policy regardless of base-tag drift.
USER 65532:65532

# 8080 = client/KV protocol, 9090 = Raft peer RPC. Informational only; the
# real bind addresses come from --addr / --raft-addr at runtime.
EXPOSE 8080 9090

# The binary is the entrypoint; the args below are a sane single-node default
# for `docker run`. In Kubernetes the StatefulSet overrides them (via args:)
# with --id/--raft-addr/--peers to form a cluster. Note 0.0.0.0, not the
# binary's localhost default: a loopback bind inside a container is
# unreachable from other pods.
ENTRYPOINT ["/raft-kv"]
CMD ["--addr", "0.0.0.0:8080", "--data", "/data"]
