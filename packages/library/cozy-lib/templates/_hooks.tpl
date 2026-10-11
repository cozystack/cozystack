{{- /*
cozy-lib.hooks.deleteSecrets renders a post-delete hook that removes Secrets
an application leaves behind on uninstall.

cert-manager runs without --enable-certificate-owner-ref, so the Secret a
Certificate writes has no owner and outlives both the Certificate and the
release. For an app that mints its own CA and leaf, those Secrets hold tls.key,
and a later release of the same name would find and reuse a key it did not
create. Turning the flag on would change the same behaviour for every
Certificate the platform itself issues, including the CA Certificates its own
components trust, so the cleanup stays with the apps that own the Secrets.

Usage from a template of the app chart, with certificates optional.

  {{- include "cozy-lib.hooks.deleteSecrets" (dict
        "root" .
        "certificates" (list (printf "%s-tls" .Release.Name))
        "secrets" (list (printf "%s-ca" .Release.Name) (printf "%s-tls" .Release.Name))) }}

secrets lists every Secret name the chart can create, whatever its current
values. The hook renders unconditionally and deletes with --ignore-not-found,
so a release that had TLS on earlier and off at uninstall is cleaned as well.
Delete is granted by explicit resourceNames rather than by a label selector.
cert-manager stamps its Secrets only with the cluster-wide
controller.cert-manager.io/fao label, which a sweep would share with every
sibling app in the namespace.

certificates lists Certificates that are not objects of the release itself,
such as the ones cert-manager's ingress-shim creates for an annotated Ingress
and hands to the Ingress as owner. Helm has already deleted the release's own
Certificates when a post-delete hook runs, but not those, and a Secret deleted
while its Certificate still exists is issued again. They are deleted first, and
a failure there leaves the Secrets in place.
*/}}
{{- define "cozy-lib.hooks.deleteSecrets" -}}
{{- $root := .root -}}
{{- $secrets := .secrets | default list -}}
{{- $certificates := .certificates | default list -}}
{{- if not (kindIs "slice" $secrets) -}}
{{- fail (printf "cozy-lib.hooks.deleteSecrets: secrets must be a list, got %s" (kindOf $secrets)) -}}
{{- end -}}
{{- if not (kindIs "slice" $certificates) -}}
{{- fail (printf "cozy-lib.hooks.deleteSecrets: certificates must be a list, got %s" (kindOf $certificates)) -}}
{{- end -}}
{{- if empty $secrets -}}
{{- fail "cozy-lib.hooks.deleteSecrets: secrets is empty, and an RBAC rule with no resourceNames grants delete on every Secret in the namespace" -}}
{{- end -}}
{{- /* The names go into the Job's shell script inside double quotes, where a
       character such as $ or a backquote would still be expanded, so only
       what Kubernetes accepts as a resource name gets through. */ -}}
{{- $resourceName := `^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$` -}}
{{- range $secrets -}}
{{- if not (and (kindIs "string" .) .) -}}
{{- fail (printf "cozy-lib.hooks.deleteSecrets: every entry of secrets must be a non-empty string, got %v" .) -}}
{{- end -}}
{{- if or (gt (len .) 253) (not (regexMatch $resourceName .)) -}}
{{- fail (printf "cozy-lib.hooks.deleteSecrets: every entry of secrets must be a Kubernetes resource name, got %q" .) -}}
{{- end -}}
{{- end -}}
{{- range $certificates -}}
{{- if not (and (kindIs "string" .) .) -}}
{{- fail (printf "cozy-lib.hooks.deleteSecrets: every entry of certificates must be a non-empty string, got %v" .) -}}
{{- end -}}
{{- if or (gt (len .) 253) (not (regexMatch $resourceName .)) -}}
{{- fail (printf "cozy-lib.hooks.deleteSecrets: every entry of certificates must be a Kubernetes resource name, got %q" .) -}}
{{- end -}}
{{- end -}}
{{- $name := printf "%s-secrets-cleanup" $root.Release.Name -}}
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ $name }}
  labels:
    app.kubernetes.io/instance: {{ $root.Release.Name }}
  annotations:
    "helm.sh/hook": post-delete
    "helm.sh/hook-weight": "0"
    "helm.sh/hook-delete-policy": before-hook-creation,hook-succeeded
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ $name }}
  labels:
    app.kubernetes.io/instance: {{ $root.Release.Name }}
  annotations:
    "helm.sh/hook": post-delete
    "helm.sh/hook-weight": "5"
    "helm.sh/hook-delete-policy": before-hook-creation,hook-succeeded
rules:
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get", "delete"]
    resourceNames:
{{- range $secrets }}
      - {{ . | quote }}
{{- end }}
{{- if $certificates }}
  - apiGroups: ["cert-manager.io"]
    resources: ["certificates"]
    verbs: ["get", "delete"]
    resourceNames:
{{- range $certificates }}
      - {{ . | quote }}
{{- end }}
{{- end }}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ $name }}
  labels:
    app.kubernetes.io/instance: {{ $root.Release.Name }}
  annotations:
    "helm.sh/hook": post-delete
    "helm.sh/hook-weight": "5"
    "helm.sh/hook-delete-policy": before-hook-creation,hook-succeeded
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: {{ $name }}
subjects:
  - kind: ServiceAccount
    name: {{ $name }}
---
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ $name }}
  labels:
    app.kubernetes.io/instance: {{ $root.Release.Name }}
  annotations:
    "helm.sh/hook": post-delete
    "helm.sh/hook-weight": "10"
    "helm.sh/hook-delete-policy": before-hook-creation,hook-succeeded
spec:
  # The delete is idempotent, so one retry is enough to tell a transient
  # apiserver error from a real one without outlasting the hook timeout.
  backoffLimit: 1
  activeDeadlineSeconds: 120
  template:
    metadata:
      labels:
        app.kubernetes.io/instance: {{ $root.Release.Name }}
        policy.cozystack.io/allow-to-apiserver: "true"
    spec:
      serviceAccountName: {{ $name }}
      restartPolicy: Never
      terminationGracePeriodSeconds: 10
      securityContext:
        runAsNonRoot: true
        runAsUser: 65534
        runAsGroup: 65534
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: cleanup
          image: docker.io/clastix/kubectl:v1.32@sha256:b9ef7d8dbe65bcc81a46c09b8dc7543103055021c4f43287bf59e92a8f4fe05c
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
          env:
            - name: HOME
              value: /tmp
          resources:
            requests:
              cpu: 10m
              memory: 64Mi
            limits:
              cpu: 200m
              memory: 128Mi
          command:
            - /bin/sh
            - -c
            - |
              set -u
              # A failed delete must reach Helm as a non-zero exit, so the Job
              # and its log stay in the namespace instead of being collected
              # by hook-succeeded. An already missing object is not a failure.
              {{- if $certificates }}
              echo "Deleting Certificates {{ join " " $certificates }} of {{ $root.Release.Name }} before their Secrets"
              if ! kubectl delete certificates.cert-manager.io --namespace {{ $root.Release.Namespace }} --ignore-not-found{{ range $certificates }} {{ . | quote }}{{ end }}; then
                echo "ERROR: failed to delete Certificates of {{ $root.Release.Name }} in {{ $root.Release.Namespace }}, so their Secrets are left in place" >&2
                exit 1
              fi
              {{- end }}
              echo "Deleting Secrets {{ join " " $secrets }} left by {{ $root.Release.Name }}"
              if kubectl delete secret --namespace {{ $root.Release.Namespace }} --ignore-not-found{{ range $secrets }} {{ . | quote }}{{ end }}; then
                echo "Secret cleanup complete."
              else
                echo "ERROR: failed to delete Secrets of {{ $root.Release.Name }} in {{ $root.Release.Namespace }}, key material may remain" >&2
                exit 1
              fi
          volumeMounts:
            - name: tmp
              mountPath: /tmp
      volumes:
        - name: tmp
          emptyDir: {}
{{- end -}}
