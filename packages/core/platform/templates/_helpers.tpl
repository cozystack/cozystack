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
Resolve networking.stageCniPlugins for a bundle, given that bundle's default.
Call as (list $ <default-bool>); emits the string "true" or "false".

Two things this exists for, both of which a bare `dig` gets wrong.

`dig` returns its default only when the key is ABSENT. A key present with a nil
value returns nil, and nil is how an operator clears an inherited override --
deleting `stageCniPlugins: true` from spec.components.platform.values by setting
it to null. A bare dig would hand that straight through as nil, the chart would
read it as off, and a generic-Linux cluster would silently go back to failing
bridge-type NADs while values.yaml documents the default as on. Nil is therefore
folded back onto the variant default, which is what "unset" means to a reader.

And the value can arrive as a string. Today a values file does not produce one
for a boolean spelling -- measured on Helm v4: yes/no/on/off and true/false all
resolve to booleans, YAML 1.1 style -- but --set-string does, and that "today"
is why the spellings are normalised rather than relied on: which of them the
parser folds is a property of whichever YAML library Helm links, not of this
chart. A parser that stopped folding yes/no would hand this template the string
instead, and normalising costs nothing either way. Since this key is how an
operator declines a behaviour change, a spelling the template does not
understand must not quietly mean the opposite of what was written: anything
unrecognised stops the render.
*/}}
{{- define "cozystack.platform.stageCniPlugins" -}}
{{- $root := index . 0 -}}
{{- $default := index . 1 -}}
{{- $value := dig "stageCniPlugins" nil ($root.Values.networking | default dict) -}}
{{- if kindIs "invalid" $value -}}
{{- $default | toString -}}
{{- else -}}
{{- $spelling := $value | toString | lower -}}
{{- if or (eq $spelling "true") (eq $spelling "yes") (eq $spelling "on") (eq $spelling "1") -}}
true
{{- else if or (eq $spelling "false") (eq $spelling "no") (eq $spelling "off") (eq $spelling "0") -}}
false
{{- else -}}
{{- fail (printf "networking.stageCniPlugins: expected a boolean, got %q -- leaving it unset follows the bundle variant" ($value | toString)) -}}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
Common system packages shared between isp-full and isp-full-generic bundles.
Does NOT include the packages each variant emits itself: networking (variant
differs), linstor (talos.enabled differs), multus (stageCniPlugins differs) and
cozystack-scheduler (emitted with its own variant in both branches)
*/}}
{{- define "cozystack.platform.system.common-packages" -}}
{{- $root := . -}}
{{include "cozystack.platform.package.default" (list "cozystack.kubeovn-webhook" $root) }}
{{include "cozystack.platform.package.default" (list "cozystack.kubeovn-plunger" $root) }}
{{include "cozystack.platform.package.default" (list "cozystack.cozy-proxy" $root) }}
{{include "cozystack.platform.package.default" (list "cozystack.metallb" $root) }}
{{include "cozystack.platform.package.default" (list "cozystack.reloader" $root) }}
{{include "cozystack.platform.package.default" (list "cozystack.linstor-scheduler" $root) }}
{{include "cozystack.platform.package.default" (list "cozystack.snapshot-controller" $root) }}
{{- /* securitygroup-controller maintains membership labels for CiliumNetworkPolicy-backed
       SecurityGroups, so it only makes sense where Cilium runs. Keeping it here with the
       other data-plane packages (rather than in the variant-agnostic body of system.yaml)
       keeps it out of the isp-hosted (networking=noop) variant, which never calls
       common-packages and has no cilium.io CRD for the controller to watch. */ -}}
{{include "cozystack.platform.package.default" (list "cozystack.securitygroup-controller" $root) }}
{{- end }}
