{{- define "cozy-lib.checkInput" }}
{{-   if not (kindIs "slice" .) }}
{{-     fail (printf "called cozy-lib function without global scope, expected [<arg>, $], got %s" (kindOf .)) }}
{{-   end }}
{{- end }}
