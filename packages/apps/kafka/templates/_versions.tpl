{{- define "kafka.versionMap" }}
{{- $versionMap := .Files.Get "files/versions.yaml" | fromYaml }}
{{- if not (hasKey $versionMap .Values.version) }}
    {{- printf `Kafka version %s is not supported, allowed versions are %v` $.Values.version (keys $versionMap | sortAlpha) | fail }}
{{- end }}
{{- index $versionMap .Values.version }}
{{- end }}

{{- /*
  Short, namespace-unique node-pool names.

  Strimzi derives every node (pod) and PVC name as <cluster>-<pool>-<id>, and the
  cluster name is the release name. Naming a pool "<release>-controller" would
  write the release name twice and blow the 63-character pod-hostname budget for
  long-but-admission-legal release names. Instead a pool name is a short
  deterministic token — a role letter plus an 8-hex digest of the release — which
  is unique within the namespace (each release hashes differently) and adds a
  fixed ~10 characters regardless of release length. The broker pool of a
  cluster migrated from ZooKeeper is the one exception: it stays exactly "kafka",
  the pool that adopted its <cluster>-kafka-N brokers (see kafka.brokerPoolName).
*/ -}}
{{- define "kafka.poolHash" -}}
{{- substr 0 8 (sha256sum .Release.Name) -}}
{{- end -}}

{{- /*
  The controller pool name is a pure function of the release, with no ownership
  lookup like kafka.brokerPoolName has: no released chart ships a controller
  pool under any other name, so no upgrade renames one.
*/ -}}
{{- define "kafka.controllerPoolName" -}}
{{- printf "c-%s" (include "kafka.poolHash" .) -}}
{{- end -}}

{{- /*
  KRaft controller quorum — fixed at creation, never re-derived on a day-2 edit.

  The quorum must be static: this chart does not resize a live KRaft quorum, so
  flipping it (e.g. because a tenant raised kafka.replicas from 1 to 3) would wedge
  the reconcile or lose the metadata quorum. So the size is pinned once: if the
  controller pool already exists, keep its current replica count; only on first
  creation is it computed — one controller for a single-node cluster
  (kafka.replicas <= 1), three otherwise (odd quorum).

  An empty lookup is safe here. Helm propagates any non-NotFound read error and
  FAILS the whole render (see pkg/engine/lookup_func.go: only apierrors.IsNotFound
  returns an empty result, every other error is returned and aborts template
  execution), so a transient apiserver/RBAC miss makes the reconcile retry rather
  than silently recompute a live quorum. An empty result therefore means a genuine
  first creation or a client-side render (helm template/unittest) that is never
  applied — either way, computing the initial value is correct.
*/ -}}
{{- define "kafka.controllerReplicas" -}}
{{- $existing := lookup "kafka.strimzi.io/v1" "KafkaNodePool" .Release.Namespace (include "kafka.controllerPoolName" .) -}}
{{- $pinned := dig "spec" "replicas" 0 ($existing | default dict) -}}
{{- if $pinned -}}{{ int $pinned }}{{- else if le (int .Values.kafka.replicas) 1 -}}1{{- else -}}3{{- end -}}
{{- end -}}

{{- /*
  Broker KafkaNodePool name: "kafka" for a cluster migrated from ZooKeeper, whose
  pool of that name adopted its <cluster>-kafka-N brokers and their PVCs, and
  the release-hashed "b-<hash>" for every other cluster.

  An empty lookup means genuine absence: Helm propagates any non-NotFound read
  error and fails the render (pkg/engine/lookup_func.go returns an empty result
  only on apierrors.IsNotFound), so a transient apiserver/RBAC miss never
  resolves a migrated cluster to "b-<hash>", which would make the hook create a
  second, empty broker pool. lookup returns nothing during `helm template`, so
  unit tests resolve to "b-<hash>".
*/ -}}
{{- define "kafka.brokerPoolName" -}}
{{- $kp := lookup "kafka.strimzi.io/v1" "KafkaNodePool" .Release.Namespace "kafka" -}}
{{- if and $kp (eq (dig "metadata" "labels" "strimzi.io/cluster" "" $kp) .Release.Name) -}}
kafka
{{- else -}}
{{- printf "b-%s" (include "kafka.poolHash" .) -}}
{{- end -}}
{{- end -}}

{{- /*
  Guard the 63-char DNS-1123 label budget for KRaft node (pod) hostnames.

  Strimzi derives node names as <cluster>-<pool>-<id>. The short hashed pool
  names keep this well under budget for almost every release name, but the very
  longest admission-legal names (a release near the 53-char Helm limit) can still
  push the derived controller node name a few characters over 63, at which point
  Strimzi cannot create the pod and the KRaft control plane silently never comes
  up. Fail the render early with an actionable message instead. The controller
  pool is checked because its node ids run highest.
*/ -}}
{{- define "kafka.assertNameLength" -}}
{{- $release := .Release.Name -}}
{{- $pool := include "kafka.controllerPoolName" . -}}
{{- $controllerReplicas := int (include "kafka.controllerReplicas" .) -}}
{{- /* Highest node id under Strimzi's contiguous per-cluster id assignment. */ -}}
{{- $maxId := sub (add (int .Values.kafka.replicas) $controllerReplicas) 1 -}}
{{- $node := printf "%s-%s-%d" $release $pool $maxId -}}
{{- if gt (len $node) 63 -}}
{{- $over := sub (len $node) 63 -}}
{{- fail (printf "kafka: release %q is too long — the derived KRaft controller node name %q is %d characters, over the 63-character Kubernetes DNS-1123 label limit for pod hostnames. Shorten the Kafka application name by at least %d character(s)." $release $node (len $node) $over) -}}
{{- end -}}
{{- end -}}
