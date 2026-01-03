{{- define "cozystack.platform.package" -}}
{{- $name := index . 0 -}}
{{- $variant := default "default" (index . 1) -}}
{{- $disabled := default (list) $.Values.bundles.disabledPackages -}}
{{- if not (has $name $disabled) -}}
---
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: {{ $name }}
spec:
  variant: {{ $variant }}
{{- end -}}
{{- end -}}

{{- define "cozystack.platform.package.default" -}}
{{- include "cozystack.platform.package" (list . "default") -}}
{{- end -}}

{{- define "cozystack.platform.package.optional" -}}
{{- $name := index . 0 -}}
{{- $variant := default "default" (index . 1) -}}
{{- $disabled := default (list) $.Values.bundles.disabledPackages -}}
{{- $enabled := default (list) $.Values.bundles.enabledPackages -}}
{{- if and (has $name $enabled) (not (has $name $disabled)) -}}
---
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: {{ $name }}
spec:
  variant: {{ $variant }}
{{- end -}}
{{- end -}}

{{- define "cozystack.platform.package.optional.default" -}}
{{- include "cozystack.platform.package.optional" (list . "default") -}}
{{- end -}}
