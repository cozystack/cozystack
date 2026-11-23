{{/*
The version the ConfigMap, its version label and the provider's spec.version all
carry. capi-operator lists every ConfigMap the selector matches and refuses a
provider while more than one is returned, before it reads spec.version
(processProviderConfigMaps). During a Helm upgrade that renames the ConfigMap the
old and the new one coexist for the length of the release, so a selector without
this label matches both and the reconcile fails.

Define it once here: a bump then moves one line, and the four references below
cannot drift apart. `make components` in this package rewrites the vendored
files, not this value.
*/}}
{{- define "capi-providers-infraprovider-proxmox.version" -}}
v0.7.7-infraprovider-proxmox
{{- end -}}
