{{- /*
  JAVA_TOOL_OPTIONS for a Strimzi-managed container.

  -XX:-UseBASE64Intrinsics turns off the AVX-512 Base64 decoder hit by
  CVE-2026-114938: a broker decodes Base64 in SASL/SCRAM before the client is
  authenticated, and the Strimzi images may bundle an OpenJDK that predates the
  fix.

  Not jvmOptions -XX: Strimzi moves those into KAFKA_JVM_PERFORMANCE_OPTS, and
  kafka-run-class.sh drops its G1 tuning when that variable is set. Every java
  in the container reads JAVA_TOOL_OPTIONS instead.
*/ -}}
{{- define "kafka.javaToolOptions" -}}
-XX:-UseBASE64Intrinsics
{{- end -}}
