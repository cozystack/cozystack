{{- /*
  Heap options for a node pool, from its sanitized resources. Strimzi sizes the
  Kafka heap by asking the image's JDK 17 for the container memory, and on
  kernels built without CONFIG_MEMCG_V1 that JDK cannot see the limit
  (JDK-8347811): it answers with host RAM, and Strimzi starts every broker
  with up to a 5Gi heap. This applies Strimzi's own rule to the real limit,
  half of it capped at 5Gi. No -XX option here: Strimzi moves every one except
  the RAM percentages into KAFKA_JVM_PERFORMANCE_OPTS, and kafka-run-class.sh
  applies its G1 tuning only while that variable is empty.
*/ -}}
{{- define "kafka.jvmOptions" -}}
{{- $memory := include "cozy-lib.resources.toFloat" .limits.memory | float64 | int64 -}}
{{- $heap := printf "%dm" (min (div $memory 2097152) 5120) -}}
{{- dict "-Xms" $heap "-Xmx" $heap | toYaml -}}
{{- end -}}
