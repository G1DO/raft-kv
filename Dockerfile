# ---- build stage ----------------------------------------------------------
# Pin to the toolchain in go.mod (go 1.25.1). Static build so the runtime
# image can be distroless/scratch-thin with no libc dependency.
FROM golang:1.25.1-alpine AS build
WORKDIR /src

# Cache module downloads. There are no third-party deps today, but this keeps
# builds fast if any are ever added.
COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/raft-kv ./cmd/server

# ---- runtime stage --------------------------------------------------------
# Alpine (not distroless) so the image ships a /bin/sh. The Kubernetes
# StatefulSet uses a shell entrypoint to derive each pod's peer list from its
# ordinal hostname; Compose passes static args and ignores the shell.
FROM alpine:3.20
WORKDIR /app

# Non-root user owning the data dir.
RUN adduser -D -u 65532 raftkv && mkdir -p /data && chown raftkv:raftkv /data

COPY --from=build /out/raft-kv /usr/local/bin/raft-kv

# Client (KV text protocol) and peer-RPC ports. These are the in-container
# bind ports used throughout the compose/k8s configs.
EXPOSE 8080 9000

# Per-node data dir (log/state/snapshot). Mount a volume here to persist.
VOLUME ["/data"]

USER raftkv
ENTRYPOINT ["/usr/local/bin/raft-kv"]
