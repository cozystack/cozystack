{{- define "kafka.versionMap" }}
{{- $versionMap := .Files.Get "files/versions.yaml" | fromYaml }}
{{- if not (hasKey $versionMap .Values.version) }}
    {{- printf `Kafka version %s is not supported, allowed versions are %v` $.Values.version (keys $versionMap | sortAlpha) | fail }}
{{- end }}
{{- index $versionMap .Values.version }}
{{- end }}

{{- /*
  KRaft controller quorum: an odd number of controllers. Single-node clusters
  (kafka.replicas <= 1) run one controller; everything else runs three. Shared
  by kafkanodepools.yaml, workloadmonitor.yaml and migration-hook.yaml so the
  three stay in lockstep.
*/ -}}
{{- define "kafka.controllerReplicas" -}}
{{- if le (int .Values.kafka.replicas) 1 -}}1{{- else -}}3{{- end -}}
{{- end -}}

{{- /*
  Broker KafkaNodePool name.

  Strimzi derives node and PVC names as <cluster>-<pool>-<id>. A pre-node-pool
  ZooKeeper cluster has brokers <cluster>-kafka-N with PVCs
  data-0-<cluster>-kafka-N, so to ADOPT those brokers and their data during the
  ZK->KRaft migration the broker pool must be named exactly "kafka". A genuinely
  fresh install has nothing to adopt and uses the release-scoped
  "<release>-broker" name, so multiple Kafka instances can coexist in one
  namespace (KafkaNodePool object names are namespace-unique).

  Detection runs at render time, before the pre-upgrade migration Job:
    - a "kafka" broker pool already exists for this cluster -> "kafka" (migrated, steady state)
    - a "<release>-broker" pool already exists              -> "<release>-broker" (fresh, steady state)
    - a Kafka CR exists but has no node pools yet           -> "kafka" (first render of a live ZK cluster about to migrate)
    - nothing exists                                        -> "<release>-broker" (fresh install)

  Collision guard: a "kafka" pool owned by a DIFFERENT cluster means a second
  Kafka is being migrated in this namespace. Strimzi node pool names are
  namespace-unique, so in-place ZK->KRaft migration of more than one Kafka per
  namespace is unsupported — fail the render loudly rather than hijack the other
  cluster's pool.

  NOTE: lookup returns nothing during `helm template`/dry-run, so this resolves
  to "<release>-broker" (the fresh default) in unit tests; the migrated ("kafka")
  path is exercised by the ZK->KRaft chainsaw upgrade replay.
*/ -}}
{{- define "kafka.brokerPoolName" -}}
{{- $ns := .Release.Namespace -}}
{{- $release := .Release.Name -}}
{{- $name := printf "%s-broker" $release -}}
{{- $kafkaPool := lookup "kafka.strimzi.io/v1beta2" "KafkaNodePool" $ns "kafka" -}}
{{- if $kafkaPool -}}
  {{- $owner := dig "metadata" "labels" "strimzi.io/cluster" "" $kafkaPool -}}
  {{- if and (ne $owner "") (ne $owner $release) -}}
    {{- fail (printf "KafkaNodePool \"kafka\" in namespace %q already belongs to cluster %q; in-place ZK->KRaft migration of more than one Kafka per namespace is unsupported because Strimzi node pool names are namespace-unique. Migrate them one at a time or in separate namespaces." $ns $owner) -}}
  {{- end -}}
  {{- $name = "kafka" -}}
{{- else if not (lookup "kafka.strimzi.io/v1beta2" "KafkaNodePool" $ns $name) -}}
  {{- if lookup "kafka.strimzi.io/v1beta2" "Kafka" $ns $release -}}
    {{- $name = "kafka" -}}
  {{- end -}}
{{- end -}}
{{- $name -}}
{{- end -}}
