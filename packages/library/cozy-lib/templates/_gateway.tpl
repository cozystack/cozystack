{{/*
Gateway API helpers.
Provide reusable templates for Gateway, HTTPRoute, and TLSRoute resources
used by tenant-level components.
*/}}

{{/*
Get the gateway-api enabled flag from cluster config.
Usage: {{ include "cozy-lib.gateway-api" . }}
*/}}
{{- define "cozy-lib.gateway-api" -}}
{{- (index .Values._cluster "gateway-api") | default "false" }}
{{- end }}

{{/*
Get the gateway class name from cluster config.
Usage: {{ include "cozy-lib.gateway-class-name" . }}
*/}}
{{- define "cozy-lib.gateway-class-name" -}}
{{- (index .Values._cluster "gateway-class-name") | default "cilium" }}
{{- end }}

{{/*
Render a complete tenant-level Gateway + HTTPRoute set with HTTP-to-HTTPS redirect.

Usage:
  {{ include "cozy-lib.gateway.httpRoute" (dict "ctx" . "name" "grafana" "hostname" $host "backendName" "grafana-service" "backendPort" 3000) }}

Parameters:
  ctx         - template context (.)
  name        - resource name for Gateway, HTTPRoute, and TLS cert Secret
  hostname    - FQDN for listeners and route matching
  backendName - backend Service name
  backendPort - backend Service port (number)
  certRef     - (optional) TLS certificate Secret name, defaults to {name}-gateway-tls
*/}}
{{- define "cozy-lib.gateway.httpRoute" -}}
{{- $ctx := .ctx }}
{{- $name := .name }}
{{- $hostname := .hostname }}
{{- $backendName := .backendName }}
{{- $backendPort := .backendPort }}
{{- $certRef := .certRef | default (printf "%s-gateway-tls" $name) }}
{{- $gatewayClassName := (index $ctx.Values._cluster "gateway-class-name") | default "cilium" }}
{{- $clusterIssuer := (index $ctx.Values._cluster "issuer-name") | default "letsencrypt-prod" }}
{{- $gateway := $ctx.Values._namespace.gateway | default "" }}
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: {{ $name }}
  annotations:
    cert-manager.io/cluster-issuer: {{ $clusterIssuer }}
spec:
  gatewayClassName: {{ $gatewayClassName }}
  infrastructure:
    labels:
      cozystack.io/gateway: {{ $gateway }}
  listeners:
  - name: http
    protocol: HTTP
    port: 80
    hostname: {{ $hostname | quote }}
    allowedRoutes:
      namespaces:
        from: Same
  - name: https
    protocol: HTTPS
    port: 443
    hostname: {{ $hostname | quote }}
    tls:
      mode: Terminate
      certificateRefs:
      - name: {{ $certRef }}
    allowedRoutes:
      namespaces:
        from: Same
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: {{ $name }}-redirect-to-https
spec:
  parentRefs:
  - name: {{ $name }}
    sectionName: http
  hostnames:
  - {{ $hostname | quote }}
  rules:
  - filters:
    - type: RequestRedirect
      requestRedirect:
        scheme: https
        statusCode: 301
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: {{ $name }}
spec:
  parentRefs:
  - name: {{ $name }}
    sectionName: https
  hostnames:
  - {{ $hostname | quote }}
  rules:
  - matches:
    - path:
        type: PathPrefix
        value: /
    backendRefs:
    - name: {{ $backendName }}
      port: {{ $backendPort }}
{{- end }}

{{/*
Render a complete tenant-level Gateway + TLSRoute set with TLS passthrough.

Usage:
  {{ include "cozy-lib.gateway.tlsRoute" (dict "ctx" . "name" "seaweedfs-filer" "hostname" $host "backendName" "filer-external" "backendPort" 18888) }}

Parameters:
  ctx         - template context (.)
  name        - resource name for Gateway and TLSRoute
  hostname    - FQDN for listener and route matching
  backendName - backend Service name
  backendPort - backend Service port (number)
*/}}
{{- define "cozy-lib.gateway.tlsRoute" -}}
{{- $ctx := .ctx }}
{{- $name := .name }}
{{- $hostname := .hostname }}
{{- $backendName := .backendName }}
{{- $backendPort := .backendPort }}
{{- $gatewayClassName := (index $ctx.Values._cluster "gateway-class-name") | default "cilium" }}
{{- $gateway := $ctx.Values._namespace.gateway | default "" }}
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: {{ $name }}
spec:
  gatewayClassName: {{ $gatewayClassName }}
  infrastructure:
    labels:
      cozystack.io/gateway: {{ $gateway }}
  listeners:
  - name: tls-passthrough
    protocol: TLS
    port: 443
    hostname: {{ $hostname | quote }}
    tls:
      mode: Passthrough
    allowedRoutes:
      namespaces:
        from: Same
---
apiVersion: gateway.networking.k8s.io/v1
kind: TLSRoute
metadata:
  name: {{ $name }}
spec:
  parentRefs:
  - name: {{ $name }}
    sectionName: tls-passthrough
  hostnames:
  - {{ $hostname | quote }}
  rules:
  - backendRefs:
    - name: {{ $backendName }}
      port: {{ $backendPort }}
{{- end }}
