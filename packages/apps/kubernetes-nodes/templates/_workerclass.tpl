{{- /* Resolve family defaults only after selection. The raw pool Values remain
       the source of presence decisions; empty maps/lists replace a baseline. */ -}}
{{- define "kubernetes-nodes.workerContext" -}}
{{- if not (hasKey . "workerConfigurationResolved") -}}
{{- $raw := deepCopy .Values -}}
{{- $class := include "kubernetes-nodes.workerClass" . | fromYaml -}}
{{- $keys := get .Values "sshKeys" | default list -}}
{{- if eq $class.bootstrap "talos" -}}
{{- if $keys -}}{{- fail (printf "WorkerClass %q: sshKeys is unsupported because Talos has no SSH server" $class.name) -}}{{- end -}}
{{- $defaults := .Files.Get "files/worker-defaults.json" | fromJson -}}
{{- $talos := deepCopy $defaults.talos -}}
{{- range $k, $v := ($class.talos | default dict) -}}{{- $_ := set $talos $k $v -}}{{- end -}}
{{- range $k, $v := ($raw.talos | default dict) -}}{{- $_ := set $talos $k $v -}}{{- end -}}
{{- if and (ne $class.name "") (not (regexMatch `^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$` (printf "%v" $talos.version))) -}}
{{- fail (printf "WorkerClass %q: talos.version %q must be a full Talos version" $class.name (printf "%v" $talos.version)) -}}
{{- end -}}
{{- $_ := set .Values "talos" $talos -}}
{{- if and (not (hasKey $raw "kernelModules")) (hasKey $class "kernelModules") -}}
{{- $_ := set .Values "kernelModules" $class.kernelModules -}}
{{- end -}}
{{- $sources := $class.substrates | default dict -}}
{{- if eq (.Values.substrate | default "kubevirt") "proxmox" -}}
{{- $proxmox := deepCopy (.Values.proxmox | default dict) -}}
{{- $fixed := $sources.proxmox | default dict -}}
{{- if and (not (hasKey $proxmox "templateTags")) $fixed -}}{{- $_ := set $proxmox "templateTags" $fixed.templateTags -}}{{- end -}}
{{- if not $proxmox.templateTags -}}{{- fail "substrate proxmox requires proxmox.templateTags: capmox clones a VM template and has no image URL to fall back on" -}}{{- end -}}
{{- if $raw.osImage -}}{{- fail "substrate proxmox does not take osImage: the disk comes from its template" -}}{{- end -}}
{{- if $fixed -}}
{{- $wanted := sortAlpha $fixed.templateTags | toJson -}}
{{- $got := sortAlpha $proxmox.templateTags | toJson -}}
{{- if or (ne $wanted $got) (ne (toString $talos.version) (toString $class.talos.version)) (ne (toString $talos.schematicID) (toString $class.talos.schematicID)) -}}
{{- fail (printf "WorkerClass %q: proxmox.templateTags or talos version/schematic conflicts with the fixed template contract" $class.name) -}}
{{- end -}}
{{- end -}}
{{- $_ := set .Values "proxmox" $proxmox -}}
{{- else -}}
{{- $kubevirt := $sources.kubevirt | default dict -}}
{{- $image := deepCopy ($kubevirt.osImage | default dict) -}}
{{- if hasKey $raw "osImage" -}}{{- $image = $raw.osImage | default dict -}}{{- end -}}
{{- $_ := set .Values "osImage" $image -}}
{{- $out := dict -}}
{{- include "kubernetes-nodes.resolveOsImage" (dict "osImage" $image "talos" $talos "out" $out "groupName" $class.name) -}}
{{- if hasKey ($kubevirt.osImage | default dict) "builtin" -}}
{{- $fixed := $kubevirt.osImage.builtin -}}
{{- if or (not $out.clone) (ne $out.version $fixed.version) (ne $out.schematicID $fixed.schematicID) -}}
{{- fail (printf "WorkerClass %q: osImage conflicts with the fixed builtin image/version/schematic contract" $class.name) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- else -}}
{{- range $field := list "talos" "kernelModules" "osImage" -}}
{{- if get $raw $field -}}{{- fail (printf "WorkerClass %q: %s is Talos-specific and unsupported on Kairos" $class.name $field) -}}{{- end -}}
{{- end -}}
{{- if gt (len $keys) 1 -}}
{{- fail (printf "sshKeys has %d keys, and a pool on the Kairos WorkerClass %q takes one: the Kairos bootstrap provider authorizes a single key" (len $keys) $class.name) -}}
{{- end -}}
{{- $_ := set .Values "talos" dict -}}
{{- end -}}
{{- $_ := set . "workerConfigurationResolved" true -}}
{{- end -}}
{{- end -}}
