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
  MIGRATED cluster is the one exception: it stays exactly "kafka" to adopt the
  existing <cluster>-kafka-N brokers and their data (see kafka.brokerPoolName).
*/ -}}
{{- define "kafka.poolHash" -}}
{{- substr 0 8 (sha256sum .Release.Name) -}}
{{- end -}}

{{- /*
  The controller pool name is a pure function of the release — no adoption/
  self-ownership lookup like kafka.brokerPoolName has, on purpose. A KRaft
  controller pool is ALWAYS created new: a fresh install makes one, and a
  ZooKeeper cluster has no controller pool to adopt (KRaft controllers do not
  exist under ZooKeeper). So there is never a pre-existing, differently-named
  controller pool for this name to preserve — the broker's "kafka"-adoption
  branch has no controller analogue because the underlying situation it handles
  (live brokers to reuse in place) has no controller counterpart. No released
  chart ships a controller pool under any other name, so no upgrade renames one.
*/ -}}
{{- define "kafka.controllerPoolName" -}}
{{- printf "c-%s" (include "kafka.poolHash" .) -}}
{{- end -}}

{{- /*
  KRaft controller quorum — fixed at creation, never re-derived on a day-2 edit.

  The quorum must be static: Strimzi 0.45 cannot scale a controller node pool, so
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
{{- $existing := lookup "kafka.strimzi.io/v1beta2" "KafkaNodePool" .Release.Namespace (include "kafka.controllerPoolName" .) -}}
{{- $pinned := dig "spec" "replicas" 0 ($existing | default dict) -}}
{{- if $pinned -}}{{ int $pinned }}{{- else if le (int .Values.kafka.replicas) 1 -}}1{{- else -}}3{{- end -}}
{{- end -}}

{{- /*
  Broker KafkaNodePool name — a short release-hashed token for fresh installs so
  several Kafka clusters can coexist in one namespace, but exactly "kafka" for a
  cluster being migrated from ZooKeeper.

  Strimzi derives node and PVC names as <cluster>-<pool>-<id>. A fresh KRaft
  cluster has no prior state to preserve, so its broker pool is named "b-<hash>"
  — unique within the namespace (each release hashes differently), letting
  multiple fresh clusters live side by side without doubling the release name
  into every pod hostname. A pre-node-pool ZooKeeper cluster, however, has
  brokers "<cluster>-kafka-N", and only a pool named exactly "kafka" adopts those
  brokers (and their PVCs) in place during migration.

  Resolution (lookup runs at render time, before the pre-upgrade migration Job):
   - this cluster already owns a "kafka" pool          -> "kafka"    (migrated, steady state)
   - this cluster already owns a "b-<hash>" pool       -> "b-<hash>" (fresh, steady state)
   - an existing non-KRaft CR with no release pool yet -> "kafka"    (classic ZK about to migrate)
   - nothing exists                                    -> "b-<hash>" (fresh install)

  Safety of the lookup: Helm propagates any non-NotFound read error and fails the
  whole render (pkg/engine/lookup_func.go returns an empty result only on
  apierrors.IsNotFound), so a transient apiserver/RBAC miss makes the reconcile
  retry — it never silently resolves a live migrated cluster to "b-<hash>" and
  lets a later upgrade prune the "kafka" pool. An empty lookup means genuine
  absence: a real fresh install, or a client-side render (helm template/unittest)
  that is never applied.

  Because "kafka" is a fixed, namespace-unique name, only ONE ZooKeeper->KRaft
  migration can run per namespace: if a "kafka" pool owned by a different cluster
  already exists, a second migration fails closed (Strimzi guidance is one Kafka
  per namespace — strimzi/strimzi-kafka-operator discussions/11120). Fresh KRaft
  clusters are never affected. lookup returns nothing during `helm template`, so
  unit tests resolve to the fresh "b-<hash>" default.
*/ -}}
{{- define "kafka.brokerPoolName" -}}
{{- $release := .Release.Name -}}
{{- $ns := .Release.Namespace -}}
{{- $brokerFresh := printf "b-%s" (include "kafka.poolHash" .) -}}
{{- $name := $brokerFresh -}}
{{- $kp := lookup "kafka.strimzi.io/v1beta2" "KafkaNodePool" $ns "kafka" -}}
{{- $kpOwner := "" -}}
{{- if $kp -}}{{- $kpOwner = dig "metadata" "labels" "strimzi.io/cluster" "" $kp -}}{{- end -}}
{{- if and $kp (eq $kpOwner $release) -}}
  {{- /* Migrated, steady state: this cluster owns the adopted "kafka" pool. */ -}}
  {{- $name = "kafka" -}}
{{- else -}}
  {{- $existingCR := lookup "kafka.strimzi.io/v1beta2" "Kafka" $ns $release -}}
  {{- $ownBrokerPool := lookup "kafka.strimzi.io/v1beta2" "KafkaNodePool" $ns $brokerFresh -}}
  {{- $kraftAnn := "" -}}
  {{- if $existingCR -}}{{- $kraftAnn = dig "metadata" "annotations" "strimzi.io/kraft" "" $existingCR -}}{{- end -}}
  {{- if $ownBrokerPool -}}
    {{- /* Fresh KRaft cluster, steady state: it already owns its "b-<hash>" pool. */ -}}
    {{- $name = $brokerFresh -}}
  {{- else if and $existingCR (ne $kraftAnn "enabled") -}}
    {{- /* Classic ZooKeeper cluster about to migrate: adopt its brokers as "kafka". */ -}}
    {{- if and $kp (ne $kpOwner "") (ne $kpOwner $release) -}}
      {{- fail (printf "cannot migrate Kafka %q: namespace %q already has a \"kafka\" broker pool owned by cluster %q. Strimzi node pool names are namespace-unique, so only one ZooKeeper->KRaft migration per namespace is possible (see strimzi/strimzi-kafka-operator discussions/11120). Migrate one at a time or use separate namespaces; fresh KRaft clusters are unaffected." $release $ns $kpOwner) -}}
    {{- end -}}
    {{- $name = "kafka" -}}
  {{- end -}}
  {{- /* else: fresh install (no CR, no own pool) -> "b-<hash>". A transient read
         error can't reach here silently: Helm propagates every non-NotFound error
         and fails the render, so an empty lookup means genuine absence (a real
         fresh install, or a client-side helm-template render that is never
         applied), never a flaked read of a live migrated cluster's "kafka" pool. */ -}}
{{- end -}}
{{- $name -}}
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
