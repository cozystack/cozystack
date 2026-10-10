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
{{- if eq $name "cozystack.networking" -}}
{{- include "cozystack.platform.networking.variantGuard" (list $root $variant) -}}
{{- end }}
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

{{/*
Refuses to change the variant of a live cozystack.networking Package. The
operator deletes every HelmRelease of a Package that its new variant does not
install, so isp-full -> isp-hosted removes cilium and kubeovn and leaves the
cluster without a CNI. The acknowledgement names the target variant rather than
being a boolean, so a value left set after one switch does not cover the next.
Inert where lookup returns nothing: helm template, client-side dry-run, first
install.
Call as (list $ <variant about to be rendered>).
*/}}
{{- define "cozystack.platform.networking.variantGuard" -}}
{{- $root := index . 0 -}}
{{- $variant := index . 1 -}}
{{- $live := lookup "cozystack.io/v1alpha1" "Package" "" "cozystack.networking" -}}
{{- if $live -}}
{{- $liveVariant := default "default" (dig "spec" "variant" "" $live) -}}
{{- $ack := dig "acknowledgeVariantChange" "" ($root.Values.networking | default dict) | toString -}}
{{- if and (ne $liveVariant $variant) (ne $ack $variant) -}}
{{- fail (printf "the live cozystack.networking Package runs variant %q and this render would switch it to %q (bundles.system.variant: %s). The operator then deletes any HelmRelease of that Package the new variant does not install (none if it installs everything the current one does): isp-full to isp-hosted removes Cilium and Kube-OVN and leaves every pod without networking, isp-full to isp-slim removes Kube-OVN. Either restore the previous spec.variant of the cozystack.cozystack-platform Package, or, once the cluster has another CNI or no longer needs the removed one, set networking.acknowledgeVariantChange: %q in its spec.components.platform.values to confirm this exact switch. If the live variant was set by hand and has to stay, add cozystack.networking to bundles.disabledPackages: acknowledging lets this render replace it." $liveVariant $variant $root.Values.bundles.system.variant $variant) -}}
{{- end -}}
{{- end -}}
{{- end }}

{{- define "cozystack.platform.package.default" -}}
{{- $name := index . 0 -}}
{{- $root := index . 1 -}}
{{- include "cozystack.platform.package" (list $name "default" $root) }}
{{ end }}

{{- define "cozystack.platform.package.optional" -}}
{{- $name := index . 0 -}}
{{- $variant := default "default" (index . 1) -}}
{{- $root := default $ (index . 2) -}}
{{- /* Components are accepted here for the same reason the always-on helper
       above accepts them: an opt-in package can need platform configuration
       just as much as a default one. Omitting this silently dropped whatever
       the caller passed, which is worse than not supporting it at all. */ -}}
{{- $components := dict -}}
{{- if gt (len .) 3 -}}
{{- $components = index . 3 -}}
{{- end -}}
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
{{- if $components }}
  components:
{{ toYaml $components | indent 4 }}
{{- end }}
{{- end }}
{{ end }}

{{- define "cozystack.platform.package.optional.default" -}}
{{- $name := index . 0 -}}
{{- $root := index . 1 -}}
{{- $components := dict -}}
{{- if gt (len .) 2 -}}
{{- $components = index . 2 -}}
{{- end -}}
{{- include "cozystack.platform.package.optional" (list $name "default" $root $components) }}
{{ end }}

{{/*
Emits "true" on the slim variants, empty otherwise.
*/}}
{{- define "cozystack.platform.slim" -}}
{{- if has .Values.bundles.system.variant (list "isp-slim" "isp-slim-generic" "isp-hosted-slim") }}true{{ end -}}
{{- end }}

{{/*
A package that is always on in the full variants and opt-in (bundles.enabledPackages)
on the slim ones. Takes the same arguments as cozystack.platform.package.
*/}}
{{- define "cozystack.platform.package.full" -}}
{{- if include "cozystack.platform.slim" (index . 2) -}}
{{- include "cozystack.platform.package.optional" . }}
{{- else -}}
{{- include "cozystack.platform.package" . }}
{{- end -}}
{{ end }}

{{/*
Emits the cozystack.networking Package. Call as
(list $ <components> <full variant> <slim variant>). The slim variants run Cilium
alone: no KubeOVN component, and Cilium L2 announcements stand in for MetalLB.
*/}}
{{- define "cozystack.platform.package.networking" -}}
{{- $root := index . 0 -}}
{{- $components := index . 1 -}}
{{- $variant := index . 2 -}}
{{- if include "cozystack.platform.slim" $root -}}
{{- if dig "encryption" "enabled" false ($root.Values.networking | default dict) -}}
{{- fail "networking.encryption.enabled is not supported on the slim variants: their Cilium-only datapath has no encryption wiring yet" -}}
{{- end -}}
{{- $variant = index . 3 -}}
{{- $_ := unset $components "kubeovn" -}}
{{- $cilium := dig "cilium" "values" "cilium" dict $components -}}
{{- $_ := set $cilium "l2announcements" (dict "enabled" true) -}}
{{- $_ := set $components "cilium" (dict "values" (dict "cilium" $cilium)) -}}
{{- end -}}
{{- include "cozystack.platform.package" (list "cozystack.networking" $variant $root $components) }}
{{- end }}

{{- define "cozystack.platform.package.full.default" -}}
{{- include "cozystack.platform.package.full" (list (index . 0) "default" (index . 1)) }}
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
Common system packages shared between the isp-full, isp-full-generic, isp-slim and
isp-slim-generic bundles.
Does NOT include the packages each variant emits itself: networking (variant
differs), linstor (talos.enabled differs), multus (stageCniPlugins differs) and
cozystack-scheduler (emitted with its own variant in both branches)
*/}}
{{- define "cozystack.platform.system.common-packages" -}}
{{- $root := . -}}
{{- if not (include "cozystack.platform.slim" $root) -}}
{{include "cozystack.platform.package.default" (list "cozystack.kubeovn-webhook" $root) }}
{{include "cozystack.platform.package.default" (list "cozystack.kubeovn-plunger" $root) }}
{{- end }}
{{include "cozystack.platform.package.full.default" (list "cozystack.cozy-proxy" $root) }}
{{include "cozystack.platform.package.full.default" (list "cozystack.metallb" $root) }}
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

{{/*
The block Cilium takes its own addresses from on the kube-ovn variants, the
last /21 of the pod CIDR, which kube-ovn is told to exclude. Called with the
pod CIDR, "cozystack.platform.ciliumPool" emits the block as a CIDR for the
Cilium operator and "cozystack.platform.ciliumPoolRange" as a first..last range
for kube-ovn excludeIps.

The block has to sit inside the pod CIDR rather than next to it. Envoy reaches
Gateway API backends from the node's Ingress IP, and kube-ovn masquerades
host-originated traffic only when its source lies in one of its subnets.
Outside them the backend's reply is routed to the backend's own node and lost.
The end of the range is the part kube-ovn reaches last, since it hands out
free addresses in ascending order and reuses released ones only once the rest
are gone. A /21 cut into /29s serves 256 nodes, the most a /16 cluster CIDR
sliced into /24s ever allowed.
*/}}
{{- define "cozystack.platform.ciliumPoolBounds" -}}
{{- $cidr := toString . -}}
{{- if not (regexMatch "^[0-9]{1,3}(\\.[0-9]{1,3}){3}/[0-9]{1,2}$" $cidr) -}}
{{- fail (printf "networking.podCIDR %q is not an IPv4 CIDR" $cidr) -}}
{{- end -}}
{{- $parts := splitList "/" $cidr -}}
{{- $prefix := atoi (last $parts) -}}
{{- if gt $prefix 20 -}}
{{- fail (printf "networking.podCIDR %s is too small. The kube-ovn variants reserve its last /21 for Cilium, so it needs a prefix of /20 or shorter" $cidr) -}}
{{- end -}}
{{- $address := 0 -}}
{{- range splitList "." (first $parts) -}}
{{- $address = add (mul $address 256) (atoi .) -}}
{{- end -}}
{{- $size := 1 -}}
{{- range until (sub 32 $prefix | int) -}}
{{- $size = mul $size 2 -}}
{{- end -}}
{{- $first := add (sub $address (mod $address $size)) (sub $size 2048) -}}
{{- printf "%s %s" (include "cozystack.platform.ipv4" $first) (include "cozystack.platform.ipv4" (add $first 2047)) -}}
{{- end }}

{{- define "cozystack.platform.ipv4" -}}
{{- printf "%d.%d.%d.%d" (div . 16777216) (mod (div . 65536) 256) (mod (div . 256) 256) (mod . 256) -}}
{{- end }}

{{- define "cozystack.platform.ciliumPool" -}}
{{- printf "%s/21" (first (splitList " " (include "cozystack.platform.ciliumPoolBounds" .))) -}}
{{- end }}

{{- define "cozystack.platform.ciliumPoolRange" -}}
{{- join ".." (splitList " " (include "cozystack.platform.ciliumPoolBounds" .)) -}}
{{- end }}
