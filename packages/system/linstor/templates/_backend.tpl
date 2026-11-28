{{- /*
Whether piraeus-operator runs in external mode against blockstor-apiserver.
That is backend blockstor once blockstorMigration is cleared, and not before:
while blockstor adopts a LINSTOR cluster's volumes, every piraeus object
(LinstorCluster, LinstorSatelliteConfiguration and what they reference)
renders exactly as under backend linstor, so the operator, which keeps
running, finds nothing to change and keeps linstor-csi on LINSTOR. Templates
of blockstor's own components follow .Values.backend instead.
*/ -}}
{{- define "linstor.piraeusExternal" -}}
{{- if and (eq .Values.backend "blockstor") (not .Values.blockstorMigration) }}true{{ end -}}
{{- end -}}
