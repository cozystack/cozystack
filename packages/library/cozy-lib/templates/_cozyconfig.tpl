{{- define "cozy-lib.loadCozyConfig" }}
{{-   include "cozy-lib.checkInput" . }}
{{-   if not (hasKey (index . 1) "cozyConfig") }}
{{-     $cozyConfig := lookup "v1" "ConfigMap" "cozy-system" "cozystack" }}
{{-     $_ := set (index . 1) "cozyConfig" $cozyConfig }}
{{-   end }}
{{- end }}
