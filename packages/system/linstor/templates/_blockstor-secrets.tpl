{{- /*
The only Secrets blockstor touches, as a YAML flow list: its keyring, the
unlock Secret it generates when no passphrase Secret is named, and the
passphrase Secret named by blockstorPassphraseSecret. Every Role that grants
blockstor Secret access names exactly these, because cozy-linstor is shared
with piraeus and its TLS keys must stay unreadable.
*/ -}}
{{- define "blockstor.secretNames" -}}
{{- $name := .Values.blockstorPassphraseSecret | toString }}
{{- if not (regexMatch "^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$" $name) }}
{{- fail (printf "linstor: blockstorPassphraseSecret must be a Secret name, got %q" $name) }}
{{- end }}
{{- list "blockstor-master-key" "blockstor-unlock" $name | uniq | toJson }}
{{- end }}
