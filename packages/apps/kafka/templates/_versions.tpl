{{- define "kafka.versionMap" }}
{{- $versionMap := .Files.Get "files/versions.yaml" | fromYaml }}
{{- if not (hasKey $versionMap .Values.version) }}
    {{- printf `Kafka version %s is not supported, allowed versions are %v` $.Values.version (keys $versionMap | sortAlpha) | fail }}
{{- end }}
{{- index $versionMap .Values.version }}
{{- end }}

{{- /*
  KRaft controller quorum — fixed at creation, never re-derived on a day-2 edit.

  The quorum must be static: Strimzi 0.45 cannot scale a controller node pool, so
  flipping it (e.g. because a tenant raised kafka.replicas from 1 to 3) would wedge
  the reconcile or lose the metadata quorum. So the size is pinned once: if the
  "controller" pool already exists, keep its current replica count; only on first
  creation is it computed — one controller for a single-node cluster
  (kafka.replicas <= 1), three otherwise (odd quorum).

  lookup returns nothing during `helm template`/dry-run, so unit tests exercise
  the creation-time computation. Shared by kafkanodepools.yaml, workloadmonitor.yaml
  and migration-hook.yaml so the three stay in lockstep.
*/ -}}
{{- define "kafka.controllerReplicas" -}}
{{- $existing := lookup "kafka.strimzi.io/v1beta2" "KafkaNodePool" .Release.Namespace (printf "%s-controller" .Release.Name) -}}
{{- $pinned := dig "spec" "replicas" 0 ($existing | default dict) -}}
{{- if $pinned -}}{{ int $pinned }}{{- else if le (int .Values.kafka.replicas) 1 -}}1{{- else -}}3{{- end -}}
{{- end -}}

{{- /*
  Broker KafkaNodePool name — release-scoped for fresh installs so several Kafka
  clusters can coexist in one namespace, but exactly "kafka" for a cluster being
  migrated from ZooKeeper.

  Strimzi derives node and PVC names as <cluster>-<pool>-<id>. A fresh KRaft
  cluster has no prior state to preserve, so its broker pool is named
  "<release>-broker" — unique within the namespace (KafkaNodePool object names
  are namespace-unique), letting multiple fresh clusters live side by side. A
  pre-node-pool ZooKeeper cluster, however, has brokers "<cluster>-kafka-N", and
  only a pool named exactly "kafka" adopts those brokers (and their PVCs) in
  place during migration.

  Resolution (lookup runs at render time, before the pre-upgrade migration Job):
   - this cluster already owns a "kafka" pool         -> "kafka" (migrated, steady state)
   - an existing non-KRaft CR with no release pool yet -> "kafka" (classic ZK about to migrate)
   - anything else                                     -> "<release>-broker" (fresh install)

  Because "kafka" is a fixed, namespace-unique name, only ONE ZooKeeper->KRaft
  migration can run per namespace: if a "kafka" pool owned by a different cluster
  already exists, a second migration fails closed (Strimzi guidance is one Kafka
  per namespace — strimzi/strimzi-kafka-operator discussions/11120). Fresh KRaft
  clusters are never affected. lookup returns nothing during `helm template`, so
  unit tests resolve to the fresh "<release>-broker" default.
*/ -}}
{{- define "kafka.brokerPoolName" -}}
{{- $release := .Release.Name -}}
{{- $ns := .Release.Namespace -}}
{{- $name := printf "%s-broker" $release -}}
{{- $kp := lookup "kafka.strimzi.io/v1beta2" "KafkaNodePool" $ns "kafka" -}}
{{- $kpOwner := "" -}}
{{- if $kp -}}{{- $kpOwner = dig "metadata" "labels" "strimzi.io/cluster" "" $kp -}}{{- end -}}
{{- if and $kp (eq $kpOwner $release) -}}
  {{- $name = "kafka" -}}
{{- else -}}
  {{- $existingCR := lookup "kafka.strimzi.io/v1beta2" "Kafka" $ns $release -}}
  {{- $ownBrokerPool := lookup "kafka.strimzi.io/v1beta2" "KafkaNodePool" $ns (printf "%s-broker" $release) -}}
  {{- $kraftAnn := "" -}}
  {{- if $existingCR -}}{{- $kraftAnn = dig "metadata" "annotations" "strimzi.io/kraft" "" $existingCR -}}{{- end -}}
  {{- if and $existingCR (ne $kraftAnn "enabled") (not $ownBrokerPool) -}}
    {{- if and $kp (ne $kpOwner "") (ne $kpOwner $release) -}}
      {{- fail (printf "cannot migrate Kafka %q: namespace %q already has a \"kafka\" broker pool owned by cluster %q. Strimzi node pool names are namespace-unique, so only one ZooKeeper->KRaft migration per namespace is possible (see strimzi/strimzi-kafka-operator discussions/11120). Migrate one at a time or use separate namespaces; fresh KRaft clusters are unaffected." $release $ns $kpOwner) -}}
    {{- end -}}
    {{- $name = "kafka" -}}
  {{- end -}}
{{- end -}}
{{- $name -}}
{{- end -}}
