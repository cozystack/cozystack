{{/*
Get IP-addresses of master nodes
*/}}
{{- define "cozystack.master-node-ips" -}}
{{- $nodes := lookup "v1" "Node" "" "" -}}
{{- $ips := list -}}
{{- range $node := $nodes.items -}}
  {{- if eq (index $node.metadata.labels "node-role.kubernetes.io/control-plane") "" -}}
    {{- range $address := $node.status.addresses -}}
      {{- if eq $address.type "InternalIP" -}}
        {{- $ips = append $ips $address.address -}}
        {{- break -}}
      {{- end -}}
    {{- end -}}
  {{- end -}}
{{- end -}}
{{ join "," $ips }}
{{- end -}}

{{- define "cozystack.defaultDashboardValues" -}}
kubeapps:
{{- if .Capabilities.APIVersions.Has "source.toolkit.fluxcd.io/v1" }}
{{- with (lookup "source.toolkit.fluxcd.io/v1" "HelmRepository" "cozy-public" "").items }}
  redis:
    master:
      podAnnotations:
        {{- range $index, $repo := . }}
        {{- with (($repo.status).artifact).revision }}
        repository.cozystack.io/{{ $repo.metadata.name }}: {{ quote . }}
        {{- end }}
        {{- end }}
{{- end }}
{{- end }}
  frontend:
    resourcesPreset: "none"
  dashboard:
    resourcesPreset: "none"
    {{- $cozystackBranding:= lookup "v1" "ConfigMap" "cozy-system" "cozystack-branding" }}
    {{- $branding := dig "data" "branding" "" $cozystackBranding }}
    {{- if $branding }}
    customLocale:
      "Kubeapps": {{ $branding }}
    {{- end }}
    customStyle: |
      {{- $logoImage := dig "data" "logo" "" $cozystackBranding }}
      {{- if $logoImage }}
      .kubeapps-logo {
        background-image: {{ $logoImage }}
      }
      {{- end }}
      #serviceaccount-selector {
        display: none;
      }
      .login-moreinfo {
        display: none;
      }
      a[href="#/docs"] {
        display: none;
      }
      .login-group .clr-form-control .clr-control-label {
        display: none;
      }
      .appview-separator div.appview-first-row div.center {
        display: none;
      }
      .appview-separator div.appview-first-row section[aria-labelledby="app-secrets"] {
        display: none;
      }
      .appview-first-row section[aria-labelledby="access-urls-title"] {
        width: 100%;
      }
{{- end }}
