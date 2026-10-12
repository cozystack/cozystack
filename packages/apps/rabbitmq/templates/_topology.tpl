{{/*
rabbitmq.topology.kinds lists the kinds messaging-topology-operator drives
through its shared TopologyReconciler. Each one carries a
deletion.finalizers.<plural>.rabbitmq.com finalizer that comes off only after a
successful management-API call, so all of them wedge the same way when the
plaintext listener closes underneath them.

SuperStream is the thirteenth CRD the operator ships and is deliberately absent:
it runs on its own reconciler, which sets no finalizer, and the Queue, Exchange
and Binding objects it owns are already on this list.

Callers read it as a space-separated list:

  {{- range $kind := splitList " " (include "rabbitmq.topology.kinds" $) -}}
*/}}
{{- define "rabbitmq.topology.kinds" -}}
Binding Exchange Federation OperatorPolicy Permission Policy Queue SchemaReplication Shovel TopicPermission User Vhost
{{- end -}}

{{/*
rabbitmq.topology.resources renders the same list as the comma-separated
argument `kubectl get` takes. The two irregular plurals match deletionFinalizer()
in the operator's controllers/utils.go, which is what names the finalizers the
runbook step is checking for.
*/}}
{{- define "rabbitmq.topology.resources" -}}
{{-   $out := list -}}
{{-   range $kind := splitList " " (include "rabbitmq.topology.kinds" $) -}}
{{-     $plural := printf "%ss" (lower $kind) -}}
{{-     if eq $kind "Policy" -}}
{{-       $plural = "policies" -}}
{{-     else if eq $kind "OperatorPolicy" -}}
{{-       $plural = "operatorpolicies" -}}
{{-     end -}}
{{-     $out = append $out (printf "%s.rabbitmq.com" $plural) -}}
{{-   end -}}
{{-   join "," $out -}}
{{- end -}}
