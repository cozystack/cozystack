{{/*
cozy-lib.gpu.entries reads a gpus list of name and className entries and
returns, as JSON, {"names": [...], "classes": [{className, count}]}. An entry
sets exactly one of name and className, count goes with className only and
defaults to 1, and a list holds one kind. Class entries fail without
_cluster.gpu-vm-enabled, since no webhook would resolve them and the VM would
boot without its GPUs. A typed client sends the zero value of every field, so
an empty string and a count of 0 read as unset.

  {{- $gpus := include "cozy-lib.gpu.entries" (list .Values.gpus .Values._cluster) | fromJson }}
*/}}
{{- define "cozy-lib.gpu.entries" -}}
{{- $gpus := index . 0 | default list -}}
{{- $cluster := index . 1 | default dict -}}
{{- $names := list -}}
{{- $classes := list -}}
{{- $units := 0 -}}
{{- range $i, $gpu := $gpus -}}
{{-   $name := $gpu.name | default "" | toString -}}
{{-   $class := $gpu.className | default "" | toString -}}
{{-   if eq (empty $name) (empty $class) -}}
{{-     fail (printf "gpus[%d]: set exactly one of name and className" $i) -}}
{{-   end -}}
{{-   $count := "1" -}}
{{-   if not (empty $gpu.count) -}}
{{-     if $name -}}
{{-       fail (printf "gpus[%d]: count applies to className entries only" $i) -}}
{{-     end -}}
{{-     $count = printf "%v" $gpu.count -}}
{{-   end -}}
{{-   if not (regexMatch "^[1-9][0-9]{0,8}$" $count) -}}
{{-     fail (printf "gpus[%d].count must be a whole number of at least 1" $i) -}}
{{-   end -}}
{{-   if $name -}}
{{-     $names = append $names $name -}}
{{-   else -}}
{{-     $units = add $units ($count | atoi) -}}
{{-     if gt $units 1024 -}}
{{-       fail "a VMI can expand at most 1024 GPU units from class entries" -}}
{{-     end -}}
{{-     $classes = append $classes (dict "className" $class "count" ($count | atoi)) -}}
{{-   end -}}
{{- end -}}
{{- if and $names $classes -}}
{{-   fail "gpus: list either only name entries or only className entries" -}}
{{- end -}}
{{- if and $classes (ne (toString (index $cluster "gpu-vm-enabled" | default "false")) "true") -}}
{{-   fail "gpus: className entries need GPUs for VMs, which this cluster does not serve; use name entries or ask the hoster to enable GPUs" -}}
{{- end -}}
{{- dict "names" $names "classes" $classes | toJson -}}
{{- end -}}
