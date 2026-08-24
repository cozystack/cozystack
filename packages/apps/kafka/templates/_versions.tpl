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
  One Kafka cluster per namespace.

  The broker KafkaNodePool must be named exactly "kafka": Strimzi derives node
  and PVC names as <cluster>-<pool>-<id>, and a pre-node-pool ZooKeeper cluster
  has brokers <cluster>-kafka-N, so only the pool name "kafka" adopts them and
  their data during the ZK->KRaft migration. Because KafkaNodePool object names
  are unique within a namespace, two Kafka clusters cannot both own a "kafka"
  pool there — this is an upstream Strimzi constraint, and Strimzi's own guidance
  is one Kafka cluster per namespace (strimzi/strimzi-kafka-operator
  discussions/11120). So this chart enforces exactly that: it fails the render if
  another Kafka cluster already lives in the namespace.

  Self is allowed (reconciles/upgrades of the same release). lookup returns
  nothing during `helm template`/dry-run, so unit tests never trip the guard.
*/ -}}
{{- define "kafka.assertSingleInstance" -}}
{{- $release := .Release.Name -}}
{{- $ns := .Release.Namespace -}}
{{- range (lookup "kafka.strimzi.io/v1beta2" "Kafka" $ns "").items -}}
  {{- if ne .metadata.name $release -}}
    {{- fail (printf "namespace %q already contains Kafka cluster %q; Cozystack runs one Kafka per namespace (Strimzi node pool names are namespace-unique — see strimzi/strimzi-kafka-operator discussions/11120). Deploy this Kafka in its own namespace." $ns .metadata.name) -}}
  {{- end -}}
{{- end -}}
{{- end -}}
