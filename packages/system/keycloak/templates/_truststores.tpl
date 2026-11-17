{{- define "keycloak.truststores.validate" -}}
{{- if not (kindIs "slice" .Values.truststores) -}}
  {{- fail "truststores must be a list" -}}
{{- end -}}
{{- range $i, $store := .Values.truststores -}}
  {{- if not (kindIs "map" $store) -}}
    {{- fail (printf "truststores[%d] must be an object" $i) -}}
  {{- end -}}
  {{- range $field, $_ := $store -}}
    {{- if not (has $field (list "secretName" "configMapName" "key")) -}}
      {{- fail (printf "truststores[%d]: unknown field %s" $i $field) -}}
    {{- end -}}
  {{- end -}}
  {{- if eq (hasKey $store "secretName") (hasKey $store "configMapName") -}}
    {{- fail (printf "truststores[%d] must set exactly one of secretName or configMapName" $i) -}}
  {{- end -}}
  {{- $name := get $store "secretName" -}}
  {{- if hasKey $store "configMapName" -}}{{- $name = $store.configMapName -}}{{- end -}}
  {{- if not (kindIs "string" $name) -}}
    {{- fail (printf "truststores[%d]: source name must be a non-empty DNS subdomain" $i) -}}
  {{- end -}}
  {{- if or (gt (len $name) 253) (not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$" $name)) -}}
    {{- fail (printf "truststores[%d]: source name must be a non-empty DNS subdomain" $i) -}}
  {{- end -}}
  {{- if hasKey $store "key" -}}
    {{- if not (kindIs "string" $store.key) -}}
      {{- fail (printf "truststores[%d]: key must contain 1-253 letters, digits, '.', '_' or '-' and cannot be '.' or start with '..'" $i) -}}
    {{- end -}}
    {{- if or (gt (len $store.key) 253) (not (regexMatch "^[A-Za-z0-9._-]+$" $store.key)) (eq $store.key ".") (hasPrefix ".." $store.key) -}}
      {{- fail (printf "truststores[%d]: key must contain 1-253 letters, digits, '.', '_' or '-' and cannot be '.' or start with '..'" $i) -}}
    {{- end -}}
  {{- end -}}
{{- end -}}
{{- end -}}
