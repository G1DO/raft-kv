#!/usr/bin/env bash
# k8s-down.sh — delete the kind cluster created by k8s-up.sh (and with it the
# StatefulSet and its per-pod PVCs).
set -euo pipefail
kind delete cluster --name raft-kv
