{{- define "cozy-lib.loadCozyConfig" }}
{{-   include "cozy-lib.checkInput" . }}
{{/* Root context is always the second element of the list */}}
{{-   $root := index . 1 }}
{{-   $target := index . 1 }}
{{-   if not (hasKey $target "cozyConfig") }}
{{/* Use _cozystack values directly, no need for data wrapper */}}
{{-     $cozyConfig := $root.Values._cozystack | default dict }}
{{-     $_ := set $target "cozyConfig" $cozyConfig }}
{{-   end }}
{{- end }}
