{{- define "namespace-annotation-webhook.name" -}}
kube-ovn-webhook
{{- end }}

{{- define "namespace-annotation-webhook.fullname" -}}
kube-ovn-webhook
{{- end }}

{{/*
The port the webhook serves on. Defined once and consumed by the container,
the Service targetPort and the Cilium clusterwide policy, because those three
must agree: the policy has default-deny disabled, so a policy pointed at a port
nothing serves denies nothing while the real port stays open -- a drift that
removes the control silently rather than breaking anything visibly.

The Service frontend port is deliberately NOT defined here. The policy denies
the backend port only, which already covers ClusterIP-addressed traffic because
Cilium translates a service to its backends before enforcing egress policy, so
there is no second value that has to stay in agreement. That translation order
is itself a pinned assumption -- see
packages/system/cilium/tests/kube_proxy_replacement_test.yaml.
*/}}
{{- define "namespace-annotation-webhook.port" -}}
8443
{{- end }}
