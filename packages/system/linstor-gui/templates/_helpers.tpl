{{/*
Common labels
*/}}
{{- define "linstor-gui.labels" -}}
app.kubernetes.io/name: linstor-gui
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: cozystack
{{- end }}

{{/*
Selector labels
*/}}
{{- define "linstor-gui.selectorLabels" -}}
app.kubernetes.io/name: linstor-gui
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Where the LINSTOR API is and which client certificate it takes, as YAML with
endpoint and clientSecret keys.

Under backend linstor that is linstor.endpoint and linstor.clientSecret,
which name the in-cluster controller and the Secret piraeus-operator issues
for it. Under backend blockstor the linstor package decides: it renders the
LinstorCluster that points piraeus-operator, and through it linstor-csi, at
the API, so this chart reads the same two fields and follows wherever they
point. The package depends on cozystack.linstor, so the LinstorCluster
exists before this chart renders; a render that finds none, or more than one,
fails rather than guess. One without externalController fails as well: on a
backend switch this release can upgrade before the linstor release has
rewritten the LinstorCluster, and falling back to the values would leave the
GUI on linstor-controller, which piraeus-operator removes, with nothing to
render it again. Failing keeps the HelmRelease retrying until the endpoint is
there.
*/}}
{{- define "linstor-gui.apiClient" -}}
{{- $backend := .Values.linstor.backend | default "linstor" -}}
{{- if not (has $backend (list "linstor" "blockstor")) -}}
{{- fail (printf "linstor.backend must be \"linstor\" or \"blockstor\", got %#v" $backend) -}}
{{- end -}}
{{- $endpoint := .Values.linstor.endpoint -}}
{{- $clientSecret := .Values.linstor.clientSecret -}}
{{- if eq $backend "blockstor" -}}
{{- $clusters := (lookup "piraeus.io/v1" "LinstorCluster" "" "").items | default list -}}
{{- if ne (len $clusters) 1 -}}
{{- fail (printf "linstor.backend is blockstor, so the LINSTOR API endpoint is read from the LinstorCluster the linstor package renders, but the cluster has %d LinstorClusters" (len $clusters)) -}}
{{- end -}}
{{- $spec := (index $clusters 0).spec | default dict -}}
{{- $endpoint = dig "externalController" "url" "" $spec -}}
{{- if not $endpoint -}}
{{- fail "linstor.backend is blockstor, but the LinstorCluster has no spec.externalController.url yet: the linstor release has not switched it to blockstor-apiserver. This release is retried until it has" -}}
{{- end -}}
{{- $clientSecret = dig "apiTLS" "clientSecretName" "linstor-client-tls" $spec -}}
{{- end -}}
endpoint: {{ $endpoint | quote }}
clientSecret: {{ $clientSecret | quote }}
{{- end }}
