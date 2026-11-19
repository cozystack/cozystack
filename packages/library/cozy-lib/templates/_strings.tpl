{{- define "cozy-lib.strings.hexToInt" }}
{{- printf "num: 0x%s" . | fromYaml | dig "num" 0 }}
{{- end }}
