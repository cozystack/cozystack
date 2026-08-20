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
