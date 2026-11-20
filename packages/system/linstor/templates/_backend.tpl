{{- /*
Whether piraeus-operator runs in external mode against blockstor-apiserver.
That is backend blockstor once blockstorMigration is cleared, and not before:
while blockstor adopts a LINSTOR cluster's volumes, every piraeus object
(LinstorCluster, LinstorSatelliteConfiguration and what they reference)
renders exactly as under backend linstor, so the operator, stopped meanwhile,
finds nothing changed when it starts again and keeps linstor-csi on LINSTOR.
Helm still patches those objects on every upgrade, which is what
piraeus-webhooks.yaml is for. Templates of blockstor's own components follow
.Values.backend instead.
*/ -}}
{{- define "linstor.piraeusExternal" -}}
{{- if and (eq .Values.backend "blockstor") (not .Values.blockstorMigration) }}true{{ end -}}
{{- end -}}
