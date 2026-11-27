{{- /* Resolve family defaults only after selection. The raw pool Values remain
       the source of presence decisions; empty lists replace a baseline, while talos
       merges per key. On a pool with a class, a value equal to one of
       legacyPools in files/worker-defaults.json is what the API stored from the
       pre-WorkerClass schema, so it counts as not set. */ -}}
{{- define "kubernetes-nodes.workerContext" -}}
{{- if not (hasKey . "workerConfigurationResolved") -}}
{{- $raw := deepCopy .Values -}}
{{- $class := include "kubernetes-nodes.workerClass" . | fromYaml -}}
{{- $keys := get .Values "sshKeys" | default list -}}
{{- range $keys -}}
{{- if or (eq (printf "%v" .) "") (contains "\n" (printf "%v" .)) (contains "\r" (printf "%v" .)) -}}{{- fail "sshKeys: a key must be a single non-empty line" -}}{{- end -}}
{{- end -}}
{{- if eq $class.bootstrap "talos" -}}
{{- if $keys -}}{{- fail (printf "%s: sshKeys is unsupported because Talos has no SSH server" (ternary "legacy pool" (printf "WorkerClass %q" $class.name) (eq $class.name ""))) -}}{{- end -}}
{{- $defaults := .Files.Get "files/worker-defaults.json" | fromJson -}}
{{- $legacyTalos := dict -}}
{{- $legacyTags := list -}}
{{- $legacyOsImage := list -}}
{{- range $defaults.legacyPools -}}
{{- range $k, $v := .talos -}}{{- $_ := set $legacyTalos $k (append (get $legacyTalos $k | default list) (toJson $v)) -}}{{- end -}}
{{- $legacyTags = append $legacyTags (toJson .proxmox.templateTags) -}}
{{- $legacyOsImage = append $legacyOsImage (toJson .osImage) -}}
{{- end -}}
{{- $classed := ne $class.name "" -}}
{{- $talos := deepCopy $defaults.talos -}}
{{- range $k, $v := ($class.talos | default dict) -}}{{- $_ := set $talos $k $v -}}{{- end -}}
{{- range $k, $v := ($raw.talos | default dict) -}}{{- if not (and $classed (has (toJson $v) (get $legacyTalos $k | default list))) -}}{{- $_ := set $talos $k $v -}}{{- end -}}{{- end -}}
{{- $_ := set .Values "talos" $talos -}}
{{- if and (not (hasKey $raw "kernelModules")) (hasKey $class "kernelModules") -}}
{{- $_ := set .Values "kernelModules" $class.kernelModules -}}
{{- end -}}
{{- $sources := $class.substrates | default dict -}}
{{- if eq (.Values.substrate | default "kubevirt") "proxmox" -}}
{{- if and (ne $class.name "") (not (regexMatch `^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9a-z.]+)?$` (printf "%v" $talos.version))) -}}
{{- $own := ($raw.talos | default dict) -}}
{{- if and (hasKey $own "version") (eq (printf "%v" $own.version) (printf "%v" $talos.version)) -}}
{{- fail (printf "pool: talos.version %q must be a full Talos release; it overrides WorkerClass %q" (printf "%v" $talos.version) $class.name) -}}
{{- end -}}
{{- fail (printf "WorkerClass %q: talos.version %q must be a full Talos version" $class.name (printf "%v" $talos.version)) -}}
{{- end -}}
{{- $proxmox := deepCopy (.Values.proxmox | default dict) -}}
{{- if and $classed (hasKey $proxmox "templateTags") (has (toJson $proxmox.templateTags) $legacyTags) -}}{{- $_ := unset $proxmox "templateTags" -}}{{- end -}}
{{- $fixed := $sources.proxmox | default dict -}}
{{- if and (not (hasKey $proxmox "templateTags")) $fixed -}}{{- $_ := set $proxmox "templateTags" $fixed.templateTags -}}{{- end -}}
{{- if not $proxmox.templateTags -}}{{- fail "substrate proxmox requires proxmox.templateTags: capmox clones a VM template and has no image URL to fall back on" -}}{{- end -}}
{{- /* The template is the boot disk, and talos.version reaches only the
       installer in the machineconfig, so a pool whose tags still name the
       previous release rolls its workers onto the old kernel while they
       claim the new one. hack/proxmox-talos-template.sh tags every template
       talos-<version>, which is the one link the chart can check; a tag set
       that names no version is the operator's call and passes. */ -}}
{{- range $proxmox.templateTags -}}
{{- if and (regexMatch "^talos-v[0-9]+\\.[0-9]+\\.[0-9]+" .) (ne . (printf "talos-%s" $talos.version)) -}}
{{- $advice := ternary "use a WorkerClass whose talos.version is the release the template carries: on a pool with a class, a talos.version an earlier schema stored counts as not set" "set talos.version to the release the template carries" $classed -}}
{{- fail (printf "proxmox.templateTags selects a template tagged %q, but talos.version is %q: the workers would boot the template's Talos and not the one their machineconfig names. Build a template for %s with hack/proxmox-talos-template.sh and list its tags, or %s." . $talos.version $talos.version $advice) -}}
{{- end -}}
{{- end -}}
{{- /* Refused rather than ignored: osImage.factory.version would still reach the
       installer image while the boot disk stays whatever the template holds. */ -}}
{{- if $raw.osImage -}}{{- fail "substrate proxmox does not take osImage: the worker disk is cloned from the Proxmox VM template, not imported from an image source. Set the Talos the template carries, and use talos.version / talos.schematicID for the in-guest installer target." -}}{{- end -}}
{{- if $fixed -}}
{{- $wanted := sortAlpha $fixed.templateTags | toJson -}}
{{- $got := sortAlpha $proxmox.templateTags | toJson -}}
{{- if or (ne $wanted $got) (ne (toString $talos.version) (toString (dig "talos" "version" "" $class))) (ne (toString $talos.schematicID) (toString (dig "talos" "schematicID" "" $class))) -}}
{{- fail (printf "WorkerClass %q: proxmox.templateTags or talos version/schematic conflicts with the fixed template contract" $class.name) -}}
{{- end -}}
{{- end -}}
{{- $_ := set .Values "proxmox" $proxmox -}}
{{- else -}}
{{- $kubevirt := $sources.kubevirt | default dict -}}
{{- $image := deepCopy ($kubevirt.osImage | default dict) -}}
{{- if and (hasKey $raw "osImage") (not (and $classed (has (toJson $raw.osImage) $legacyOsImage))) -}}{{- $image = $raw.osImage | default dict -}}{{- end -}}
{{- $_ := set .Values "osImage" $image -}}
{{- $out := dict -}}
{{- include "kubernetes-nodes.resolveOsImage" (dict "osImage" $image "talos" $talos "out" $out "groupName" (trimPrefix (printf "kubernetes-nodes-%v-" .Values.cluster) .Release.Name)) -}}
{{- if hasKey ($kubevirt.osImage | default dict) "builtin" -}}
{{- /* Read by the golden-image errors in nodegroup.yaml only: this class
       refuses every other image source, so their pool-level advice would not
       apply. Kept off .Values so no rendered object can carry it. */ -}}
{{- $_ := set . "workerImageFixedByClass" $class.name -}}
{{- $fixed := $kubevirt.osImage.builtin -}}
{{- if or (not $out.clone) (ne $out.version $fixed.version) (ne $out.schematicID $fixed.schematicID) -}}
{{- fail (printf "WorkerClass %q: osImage conflicts with the fixed builtin image/version/schematic contract" $class.name) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- else -}}
{{- range $field := list "kernelModules" "osImage" -}}
{{- if get $raw $field -}}{{- fail (printf "WorkerClass %q: %s is Talos-specific and unsupported on Kairos" $class.name $field) -}}{{- end -}}
{{- end -}}
{{- /* The chart defaults leave empty talos members on every pool; only a
       member with a value is Talos configuration. */ -}}
{{- range $k, $v := ($raw.talos | default dict) -}}
{{- if $v -}}{{- fail (printf "WorkerClass %q: talos is Talos-specific and unsupported on Kairos" $class.name) -}}{{- end -}}
{{- end -}}
{{- if get $raw "gpus" -}}{{- fail (printf "WorkerClass %q: gpus: Kairos workers have no NVIDIA driver path; use a Talos class for a GPU pool" $class.name) -}}{{- end -}}
{{- if gt (len $keys) 1 -}}
{{- fail (printf "sshKeys has %d keys, and a pool on the Kairos WorkerClass %q takes one: the Kairos bootstrap provider authorizes a single key" (len $keys) $class.name) -}}
{{- end -}}
{{- $_ := set .Values "talos" dict -}}
{{- end -}}
{{- $_ := set . "workerConfigurationResolved" true -}}
{{- end -}}
{{- end -}}
