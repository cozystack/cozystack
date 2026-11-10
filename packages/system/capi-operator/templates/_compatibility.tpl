{{- define "cozystack.capi-operator.legacyFeatureGates" -}}
{{- $manager := deepCopy (.provider.manager | default dict) -}}
{{- $legacy := dig "featureGates" .key (dict) (.root.Values.manager | default dict) -}}
{{- if and (not (hasKey $manager "featureGates")) $legacy -}}
{{- $_ := set $manager "featureGates" (deepCopy $legacy) -}}
{{- $_ := set .provider "manager" $manager -}}
{{- end -}}
{{- end -}}
