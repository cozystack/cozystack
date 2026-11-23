{{/*
The version the ConfigMap, its version label and the provider's spec.version all
carry, defined once so a bump moves one line. The version label is what keeps a
renamed ConfigMap from matching twice: capi-operator lists every ConfigMap the
selector matches and refuses a provider while more than one is returned, before
it reads spec.version (processProviderConfigMaps). During a Helm upgrade that
renames the ConfigMap the old and the new one coexist for the length of the
release, so a selector without this label matches both and the reconcile fails.
*/}}
{{- define "capi-providers-cpprovider.version" -}}
v0.19.0-cozystack.0
{{- end -}}
