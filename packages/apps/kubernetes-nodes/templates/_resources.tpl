{{/*
Convert a CPU quantity string (e.g. "100m", "0.5", "1", "4") to millicores.
Returns a number string: "100", "500", "1000", "4000".
*/}}
{{- define "kubernetes.cpuToMillicores" -}}
{{-   $str := . | toString -}}
{{-   if hasSuffix "m" $str -}}
{{-     trimSuffix "m" $str -}}
{{-   else -}}
{{-     mulf ($str | float64) 1000.0 | int | toString -}}
{{-   end -}}
{{- end -}}
