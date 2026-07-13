{{/*
Resource name (short). Used as the immutable `app:` label/selector value and as
the pod-DNS prefix, so it must stay stable across upgrades.
*/}}
{{- define "raft-kv.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully-qualified name. Names the StatefulSet, the headless Service, and
serviceName — all three MUST be equal so per-pod DNS resolves as
<fullname>-<ordinal>.<fullname>. Defaults to the release name, collapsing to
"raft-kv" when the release is also named raft-kv (so `helm install raft-kv .`
yields the stable pod names raft-kv-0..N over the headless Service raft-kv).
*/}}
{{- define "raft-kv.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Labels. The original manifest uses a single `app:` key for both metadata labels
and selectors; selectors are immutable, so this stays a minimal, stable set.
*/}}
{{- define "raft-kv.labels" -}}
app: {{ include "raft-kv.name" . }}
{{- end -}}

{{/*
The cluster-wide --peers string handed identically to every pod.

Each member is "<id>@<raftAddr>@<clientAddr>" (parsePeers, cmd/server/main.go),
where id == the pod name == that pod's --id, so each node drops its own entry
and sizes quorum as len(peers)+1. Host is the StatefulSet's stable per-pod DNS,
<fullname>-<ordinal>.<fullname>. Regenerated from replicaCount, so peers and
replicas can never disagree.
*/}}
{{- define "raft-kv.peers" -}}
{{- $fullname := include "raft-kv.fullname" . -}}
{{- $client := int .Values.ports.client -}}
{{- $raft := int .Values.ports.raft -}}
{{- $peers := list -}}
{{- range $i := until (int .Values.replicaCount) -}}
{{- $host := printf "%s-%d.%s" $fullname $i $fullname -}}
{{- $peers = append $peers (printf "%s-%d@%s:%d@%s:%d" $fullname $i $host $raft $host $client) -}}
{{- end -}}
{{- join "," $peers -}}
{{- end -}}

{{/*
OTLP TCP port for NetworkPolicy egress. Prefer the port in tracing.otlpEndpoint;
fall back to networkPolicy.otlp.port when the endpoint has no ":port" suffix.
*/}}
{{- define "raft-kv.otlpPort" -}}
{{- $ep := .Values.tracing.otlpEndpoint -}}
{{- if contains ":" $ep -}}
{{- (split ":" $ep)._1 -}}
{{- else -}}
{{- .Values.networkPolicy.otlp.port -}}
{{- end -}}
{{- end -}}

{{/*
Workload ServiceAccount name. create=true => dedicated SA (default: fullname).
*/}}
{{- define "raft-kv.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "raft-kv.fullname" .) .Values.serviceAccount.name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
