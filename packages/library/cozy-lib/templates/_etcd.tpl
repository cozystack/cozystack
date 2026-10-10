{{/*
Shapes of a Cozystack etcd: the EtcdCluster, its cert-manager PKI, the Kamaji
DataStore pointing at it and the satellites that keep it healthy. The etcd
chart renders one per tenant under the fixed name "etcd"; the kubernetes chart
renders one per cluster under its own name. Every name below derives from
.name, so the etcd chart's output is unchanged by the extraction.

Each template takes a dict:
  name    - EtcdCluster name; the operator names its Services and member Pods
            after it, and every Secret, Issuer and Certificate here is
            "<name>-<suffix>".
  root    - the calling chart's root context ($), for .Release, .Chart and
            the _cluster values.
  values  - (cluster, vpa) version, replicas, size, storageClass, resources,
            affinity, in the etcd chart's values shape.
  labels  - (cluster) additionalMetadata labels the operator copies onto every
            member Pod and PVC.
  extraDNSNames - (certificates) SANs appended to the server and peer
            certificates.
  dataStoreName - (datastore) name of the cluster-scoped DataStore.
  podLabels - (defrag) extra labels on the defrag Job's Pods.
*/}}

{{- define "cozy-lib.etcd.quotaBackendBytes" -}}
{{- $units := dict "Ki" 1024 "Mi" 1048576 "Gi" 1073741824 -}}
{{- $value := regexFind "[0-9.]+" . -}}
{{- $unit := regexFind "[a-zA-Z]+" . -}}
{{- $numericValue := float64 $value -}}
{{- $bytes := mulf $numericValue (index $units $unit) -}}
{{- $result := mulf $bytes 0.95 -}}
{{- printf "%.0f" $result -}}
{{- end -}}

{{- define "cozy-lib.etcd.cluster" -}}
{{- $name := .name -}}
{{- $values := .values -}}
{{- $root := .root -}}
apiVersion: etcd-operator.cozystack.io/v1alpha2
kind: EtcdCluster
metadata:
  name: {{ $name }}
spec:
  version: {{ $values.version | quote }}
  replicas: {{ $values.replicas }}
  storage:
    size: {{ $values.size }}
    {{- with $values.storageClass }}
    storageClassName: {{ . }}
    {{- end }}
  options:
    # quotaBackendBytes/snapshotCount are typed integers in v1alpha2 (no quotes).
    quotaBackendBytes: {{ include "cozy-lib.etcd.quotaBackendBytes" $values.size }}
    autoCompactionMode: periodic
    autoCompactionRetention: "5m"
    snapshotCount: 10000
  {{- with $values.resources }}
  resources: {{- include "cozy-lib.resources.sanitize" (list . $root) | nindent 4 }}
  {{- end }}
  {{- with .labels }}
  additionalMetadata:
    labels:
      {{- toYaml . | nindent 6 }}
  {{- end }}
  # TLS uses secretRef mode: the chart issues the server, operator-client and
  # peer certs via the cert-manager Certificates rendered below, and the
  # EtcdCluster only REFERENCES the resulting Secrets. This is deliberate, not
  # cosmetic — spec.tls is immutable in v1alpha2 (CRD CEL: self.tls ==
  # oldSelf.tls), and `etcd-migrate` adopts legacy clusters into exactly this
  # secretRef shape (serverSecretRef/operatorClientSecretRef/peer.secretRef,
  # pointing at the legacy etcd-{server,client,peer}-tls Secrets). Rendering
  # certManager here would diverge from the adopted object and the API server
  # would reject every post-adoption HelmRelease reconcile, leaving migrated
  # clusters permanently non-Ready. Providing operatorClientSecretRef keeps etcd
  # --client-cert-auth on, so external clients (the Kamaji DataStore) still
  # authenticate with a CN=root client cert. ca.crt lives inside each referenced
  # Secret (cert-manager writes it), which is what v1alpha2 reads as the trusted
  # CA. No spec.auth: cozystack stays cert-only (no etcd password auth).
  tls:
    client:
      serverSecretRef:
        name: {{ $name }}-server-tls
      operatorClientSecretRef:
        name: {{ $name }}-client-tls
    peer:
      secretRef:
        name: {{ $name }}-peer-tls
  {{- $rawConstraints := "" }}
  {{- with dig "scheduling" "" ($root.Values._cluster | default dict) }}
    {{- $rawConstraints = get . "globalAppTopologySpreadConstraints" }}
  {{- end }}
  {{- if $rawConstraints }}
  {{- /* The raw value already carries its own top-level topologySpreadConstraints: key */}}
  {{- $rawConstraints | fromYaml | toYaml | nindent 2 }}
    labelSelector:
      matchLabels:
        app.kubernetes.io/instance: {{ $name }}
  {{- else }}
  topologySpreadConstraints:
  - maxSkew: 1
    topologyKey: "kubernetes.io/hostname"
    whenUnsatisfiable: ScheduleAnyway
    labelSelector:
      matchLabels:
        app.kubernetes.io/instance: {{ $name }}
  {{- end }}
  {{- with $values.affinity }}
  affinity:
    {{- toYaml . | nindent 4 }}
  {{- else }}
  # Soft anti-affinity: spread etcd members across nodes on a best-effort
  # basis so a single node failure is unlikely to take out more than one
  # member. Preferred (not required) because a hard rule would leave a member
  # Pending when replicas exceed schedulable nodes, or when node-local storage
  # pins a member to a node. Override via .Values.affinity for a hard rule.
  affinity:
    podAntiAffinity:
      preferredDuringSchedulingIgnoredDuringExecution:
        - weight: 100
          podAffinityTerm:
            labelSelector:
              matchLabels:
                app.kubernetes.io/instance: {{ $name }}
            topologyKey: kubernetes.io/hostname
  {{- end }}
{{- end -}}

{{- define "cozy-lib.etcd.certificates" -}}
{{- $name := .name -}}
{{- $root := .root -}}
{{- $ns := $root.Release.Namespace -}}
{{- $dnsNames := list $name (printf "%s.%s.svc" $name $ns) (printf "*.%s.%s.svc" $name $ns) -}}
{{- $dnsNames = concat $dnsNames (.extraDNSNames | default list) (list "localhost") -}}
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: {{ $name }}-selfsigning-issuer
spec:
  selfSigned: {}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: {{ $name }}-peer-ca
spec:
  isCA: true
  usages:
  - "signing"
  - "key encipherment"
  - "cert sign"
  commonName: {{ $name }}-peer-ca
  duration: 87600h
  subject:
    organizations:
      - {{ $ns }}
    organizationalUnits:
      - {{ $root.Release.Name }}
  secretName: {{ $name }}-peer-ca-tls
  privateKey:
    rotationPolicy: Never
    algorithm: RSA
    size: 4096
  issuerRef:
    name: {{ $name }}-selfsigning-issuer
    kind: Issuer
    group: cert-manager.io
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: {{ $name }}-ca
spec:
  isCA: true
  usages:
  - "signing"
  - "key encipherment"
  - "cert sign"
  commonName: {{ $name }}-ca
  duration: 87600h
  subject:
    organizations:
      - {{ $ns }}
    organizationalUnits:
      - {{ $root.Release.Name }}
  secretName: {{ $name }}-ca-tls
  privateKey:
    rotationPolicy: Never
    algorithm: RSA
    size: 4096
  issuerRef:
    name: {{ $name }}-selfsigning-issuer
    kind: Issuer
    group: cert-manager.io
---
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: {{ $name }}-peer-issuer
spec:
  ca:
    secretName: {{ $name }}-peer-ca-tls
---
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: {{ $name }}-issuer
spec:
  ca:
    secretName: {{ $name }}-ca-tls
---
# Server certificate, referenced by spec.tls.client.serverSecretRef. The SANs
# must cover every name the operator and clients dial the cluster by. We use
# WILDCARDS (not the legacy chart's enumerated etcd-<i>.etcd-headless SANs)
# because the v1alpha2 operator brings up members — including replacements after
# a failure — under random names in its native domain (<member>.etcd.<ns>.svc);
# an enumerated list would leave a replacement member's name out of every SAN
# and fail TLS verification forever (a silent one-short-cluster). The legacy
# headless wildcard is kept so adopted members still validate during the
# transition window. cert-manager writes ca.crt into etcd-server-tls, which
# v1alpha2 reads as --trusted-ca-file.
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: {{ $name }}-server
spec:
  commonName: {{ $name }}-server
  secretName: {{ $name }}-server-tls
  subject:
    organizations:
      - {{ $ns }}
    organizationalUnits:
      - {{ $root.Release.Name }}
  isCA: false
  usages:
  - "client auth"
  - "server auth"
  - "signing"
  - "key encipherment"
  dnsNames:
  {{- range $dnsNames }}
  - {{ if hasPrefix "*" . }}{{ . | quote }}{{ else }}{{ . }}{{ end }}
  {{- end }}
  ipAddresses:
  - "127.0.0.1"
  privateKey:
    rotationPolicy: Always
    algorithm: RSA
    size: 4096
  issuerRef:
    name: {{ $name }}-issuer
    kind: Issuer
---
# Peer certificate, referenced by spec.tls.peer.secretRef. Same wildcard SAN
# rationale as the server cert above.
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: {{ $name }}-peer
spec:
  commonName: {{ $name }}-peer
  secretName: {{ $name }}-peer-tls
  subject:
    organizations:
      - {{ $ns }}
    organizationalUnits:
      - {{ $root.Release.Name }}
  isCA: false
  usages:
  - "server auth"
  - "client auth"
  - "signing"
  - "key encipherment"
  dnsNames:
  {{- range $dnsNames }}
  - {{ if hasPrefix "*" . }}{{ . | quote }}{{ else }}{{ . }}{{ end }}
  {{- end }}
  ipAddresses:
  - "127.0.0.1"
  privateKey:
    rotationPolicy: Always
    algorithm: RSA
    size: 4096
  issuerRef:
    name: {{ $name }}-peer-issuer
    kind: Issuer
---
# Client certificate, referenced by spec.tls.client.operatorClientSecretRef and
# reused by external consumers of the cluster (the Kamaji DataStore).
# commonName=root identifies the caller as the etcd root user under
# --client-cert-auth.
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: {{ $name }}-client
spec:
  commonName: root
  secretName: {{ $name }}-client-tls
  subject:
    organizations:
      - {{ $ns }}
    organizationalUnits:
      - {{ $root.Release.Name }}
  usages:
  - "signing"
  - "key encipherment"
  - "client auth"
  privateKey:
    rotationPolicy: Always
    algorithm: RSA
    size: 4096
  issuerRef:
    name: {{ $name }}-issuer
    kind: Issuer
{{- end -}}

{{- define "cozy-lib.etcd.workloadMonitor" -}}
{{- $name := .name -}}
{{- $root := .root -}}
apiVersion: cozystack.io/v1alpha1
kind: WorkloadMonitor
metadata:
  name: {{ $name }}
  namespace: {{ $root.Release.Namespace }}
spec:
  replicas: {{ .values.replicas }}
  minReplicas: {{ div .values.replicas 2 | add1 }}
  kind: etcd
  type: etcd
  selector:
    app.kubernetes.io/instance: {{ $name }}
    app.kubernetes.io/managed-by: etcd-operator
    app.kubernetes.io/name: etcd
  version: {{ $root.Chart.Version }}
{{- end -}}

{{- define "cozy-lib.etcd.datastore" -}}
{{- $name := .name -}}
{{- $ns := .root.Release.Namespace -}}
apiVersion: kamaji.clastix.io/v1alpha1
kind: DataStore
metadata:
  name: {{ .dataStoreName }}
spec:
  driver: etcd
  endpoints:
  - {{ $name }}.{{ $ns }}.svc:2379
  tlsConfig:
    certificateAuthority:
      certificate:
        secretReference:
          keyPath: tls.crt
          name: {{ $name }}-ca-tls
          namespace: {{ $ns }}
      privateKey:
        secretReference:
          keyPath: tls.key
          name: {{ $name }}-ca-tls
          namespace: {{ $ns }}
    clientCertificate:
      certificate:
        secretReference:
          keyPath: tls.crt
          name: {{ $name }}-client-tls
          namespace: {{ $ns }}
      privateKey:
        secretReference:
          keyPath: tls.key
          name: {{ $name }}-client-tls
          namespace: {{ $ns }}
{{- end -}}

{{- define "cozy-lib.etcd.defrag" -}}
{{- $name := .name -}}
{{- $root := .root -}}
apiVersion: batch/v1
kind: CronJob
metadata:
  name: {{ $name }}-defrag
spec:
  schedule: "0 * * * *"
  concurrencyPolicy: Forbid
  startingDeadlineSeconds: 300
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 1
  jobTemplate:
    spec:
      activeDeadlineSeconds: 1800
      backoffLimit: 2
      template:
        {{- with .podLabels }}
        metadata:
          labels:
            {{- toYaml . | nindent 12 }}
        {{- end }}
        spec:
          securityContext:
            runAsNonRoot: true
            runAsUser: 1001
            seccompProfile:
              type: RuntimeDefault
          containers:
          - name: etcd-defrag
            image: ghcr.io/ahrtr/etcd-defrag:v0.13.0
            securityContext:
              allowPrivilegeEscalation: false
              readOnlyRootFilesystem: true
              capabilities:
                drop: ["ALL"]
            resources:
              requests:
                cpu: 200m
                memory: 256Mi
              limits:
                cpu: 500m
                memory: 512Mi
            args:
            # The v1alpha2 operator names the headless Service after the
            # cluster ({{ $name }}) and does not use StatefulSet
            # ordinal pod names. Point at the headless Service and let
            # --cluster fan the defrag out to every member via member-list.
            - --endpoints=https://{{ $name }}.{{ $root.Release.Namespace }}.svc:2379
            - --cacert=/etc/etcd/pki/client/cert/ca.crt
            - --cert=/etc/etcd/pki/client/cert/tls.crt
            - --key=/etc/etcd/pki/client/cert/tls.key
            - --cluster
            - --defrag-rule
            - "dbQuotaUsage > 0.8 || dbSize - dbSizeInUse > 200*1024*1024"
            volumeMounts:
            - mountPath: /etc/etcd/pki/client/cert
              name: client-certificate
              readOnly: true
          volumes:
          - name: client-certificate
            secret:
              secretName: {{ $name }}-client-tls
          restartPolicy: OnFailure
{{- end -}}

{{- define "cozy-lib.etcd.vpa" -}}
{{- $name := .name -}}
{{- $root := .root -}}
apiVersion: autoscaling.k8s.io/v1
kind: VerticalPodAutoscaler
metadata:
  name: {{ $name }}
spec:
  # The v1alpha2 operator manages member Pods directly (no StatefulSet); the
  # EtcdCluster exposes a /scale subresource with status.selector for HPA/VPA.
  targetRef:
    apiVersion: etcd-operator.cozystack.io/v1alpha2
    kind: EtcdCluster
    name: {{ $name }}
  updatePolicy:
    # Initial, not Auto: Auto evicts running etcd members to apply new
    # recommendations, churning the StatefulSet (and tripping the Cilium
    # IP-reuse leak under load). Evicting an etcd member is also disruptive to
    # quorum. Initial sets requests at pod creation without evicting; the
    # minAllowed floor below is the safety net since running pods aren't resized.
    updateMode: Initial
  resourcePolicy:
    containerPolicies:
    - containerName: etcd
      {{- with dict "cpu" "250m" "memory" "256Mi" }}
      minAllowed: {{- get (include "cozy-lib.resources.sanitize" (list . $root) | fromYaml) "requests" | toYaml | nindent 8 }}
      {{- end }}
      {{- with dict "cpu" "5000m" "memory" "8Gi" }}
      maxAllowed: {{- get (include "cozy-lib.resources.sanitize" (list . $root) | fromYaml) "requests" | toYaml | nindent 8 }}
      {{- end }}
{{- end -}}

{{- define "cozy-lib.etcd.podScrape" -}}
apiVersion: operator.victoriametrics.com/v1beta1
kind: VMPodScrape
metadata:
  name: {{ .name }}-pod-scrape
spec:
  podMetricsEndpoints:
    - port: metrics
      scheme: http
  selector:
    matchLabels:
      {{- toYaml .selector | nindent 6 }}
{{- end -}}

{{- /* The etcd chart's default version; bump the two together. */ -}}
{{- define "cozy-lib.etcd.defaultVersion" -}}
3.6.11
{{- end -}}
