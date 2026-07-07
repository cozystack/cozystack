{{- define "cozystack.platform.package" -}}
{{- $name := index . 0 -}}
{{- $variant := default "default" (index . 1) -}}
{{- $root := default $ (index . 2) -}}
{{- $components := dict -}}
{{- if gt (len .) 3 -}}
{{- $components = index . 3 -}}
{{- end -}}
{{- $disabled := default (list) $root.Values.bundles.disabledPackages -}}
{{- if not (has $name $disabled) -}}
---
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: {{ $name }}
  annotations:
    helm.sh/resource-policy: keep
spec:
  variant: {{ $variant }}
{{- if $components }}
  components:
{{ toYaml $components | indent 4 }}
{{- end }}
{{- end }}
{{ end }}

{{- define "cozystack.platform.package.default" -}}
{{- $name := index . 0 -}}
{{- $root := index . 1 -}}
{{- include "cozystack.platform.package" (list $name "default" $root) }}
{{ end }}

{{- define "cozystack.platform.package.optional" -}}
{{- $name := index . 0 -}}
{{- $variant := default "default" (index . 1) -}}
{{- $root := default $ (index . 2) -}}
{{- $disabled := default (list) $root.Values.bundles.disabledPackages -}}
{{- $enabled := default (list) $root.Values.bundles.enabledPackages -}}
{{- if and (has $name $enabled) (not (has $name $disabled)) -}}
---
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: {{ $name }}
  annotations:
    helm.sh/resource-policy: keep
spec:
  variant: {{ $variant }}
{{- end }}
{{ end }}

{{- define "cozystack.platform.package.optional.default" -}}
{{- $name := index . 0 -}}
{{- $root := index . 1 -}}
{{- include "cozystack.platform.package.optional" (list $name "default" $root) }}
{{ end }}

{{/*
Common system packages shared between isp-full and isp-full-generic bundles.
Does NOT include: networking (variant differs), linstor (talos.enabled differs)
*/}}
{{- define "cozystack.platform.system.common-packages" -}}
{{- $root := . -}}
{{- $networkingVariant := $root.Values.bundles.system.networkingVariant | default "kubeovn-cilium" -}}
{{- /* Cilium/kube-ovn-coupled data-plane packages. The cozyplane variant runs no
       Cilium and no kube-ovn, so these are orphaned there and MUST be skipped:
       - kubeovn-webhook/-plunger: kube-ovn control plane (and a fail-closed
         admission webhook once cert-manager lands would block resource creation);
       - multus: delegates to the Cilium primary conflist (05-cilium.conflist),
         which cozyplane never writes → CrashLoopBackOff;
       - securitygroup-controller: projects sdn.cozystack.io SecurityGroups onto
         CiliumNetworkPolicy (no such CRD here → crash), AND collides with
         cozyplane, which owns the whole sdn.cozystack.io group. */ -}}
{{- if ne $networkingVariant "cozyplane" }}
{{include "cozystack.platform.package.default" (list "cozystack.kubeovn-webhook" $root) }}
{{include "cozystack.platform.package.default" (list "cozystack.kubeovn-plunger" $root) }}
{{include "cozystack.platform.package.default" (list "cozystack.multus" $root) }}
{{include "cozystack.platform.package.default" (list "cozystack.securitygroup-controller" $root) }}
{{- end }}
{{include "cozystack.platform.package.default" (list "cozystack.cozy-proxy" $root) }}
{{include "cozystack.platform.package.default" (list "cozystack.metallb" $root) }}
{{include "cozystack.platform.package.default" (list "cozystack.reloader" $root) }}
{{include "cozystack.platform.package.default" (list "cozystack.linstor-scheduler" $root) }}
{{include "cozystack.platform.package.default" (list "cozystack.snapshot-controller" $root) }}
{{- end }}
