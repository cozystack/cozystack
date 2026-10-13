{{- /*
  Spec of the KairosConfigTemplate a `kairos` pool joins through. The
  kairos-reconcile Job applies it from a ConfigMap and fills in, with
  envsubst, only the runtime facts it names: ${SVC_IP} (the apiserver Service
  ClusterIP), ${COREDNS_IP} (the management CoreDNS ClusterIP), ${RELEASE} and
  ${NS}. They are kept out of the hash for the same reason as in the Talos
  template: hashing them would roll every worker on a Service re-address. No
  value here passes through a shell, so tenant input needs YAML quoting only.

  The worker joins https://<release>.<ns>.svc:6443 by name. kubeadm also
  reads the server name from the cluster-info ConfigMap the control plane
  publishes, so the name has to resolve during the join itself. On the first
  boot the files below are written after systemd-resolved has started, and a
  resolved drop-in takes effect only from the next boot; /etc/hosts is read
  on every lookup, so the apiserver entry there is what makes the first join
  work.

  Input: a dict with `root` and `group`.
*/}}
{{- define "kubernetes.kairosConfigTemplateSpec" -}}
{{- $root := .root }}
{{- $group := .group }}
{{- $kubeletVersion := include "kubernetes.versionMap" $root | trim }}
{{- $dnsDomain := include "kubernetes.tenantClusterDomain" $root }}
{{- $mgmtClusterDomain := (index ($root.Values._cluster | default dict) "cluster-domain") | default "cozy.local" }}
{{- $searchDomains := list (printf "svc.%s" $mgmtClusterDomain) $mgmtClusterDomain }}
{{- if ne $mgmtClusterDomain "cluster.local" }}
{{-   $searchDomains = concat $searchDomains (list "svc.cluster.local" "cluster.local") }}
{{- end }}
{{- $r := include "kubernetes-nodes.kubeletReservations" (dict "root" $root "group" $group) | fromYaml -}}
spec:
  template:
    spec:
      role: worker
      distribution: kubeadm
      kubernetesVersion: "{{ $kubeletVersion }}"
      serverAddress: "https://${RELEASE}.${NS}.svc:6443"
      userName: kairos
      userGroups:
        - admin
      {{- /* The provider refuses a worker without a credential. "*" goes
             into /etc/shadow as is and matches no password, so there is no
             password login; unlike "!", sshd does not treat it as a locked
             account, so sshPublicKey still works. */}}
      userPassword: "*"
      {{- with $root.Values.kairos.sshPublicKey }}
      sshPublicKey: {{ . | quote }}
      {{- end }}
      files:
        - path: /etc/hosts
          permissions: "0644"
          owner: root:root
          content: |
            127.0.0.1 localhost
            ::1 localhost ip6-localhost ip6-loopback
            ${SVC_IP} ${RELEASE}.${NS}.svc ${RELEASE}.${NS}.svc.{{ $dnsDomain }}
        - path: /etc/systemd/resolved.conf.d/cozystack.conf
          permissions: "0644"
          owner: root:root
          content: |
            [Resolve]
            DNS=${COREDNS_IP}
            Domains={{ join " " $searchDomains }}
        - path: /etc/kubernetes/kairos-patches/kubeletconfiguration1+merge.yaml
          permissions: "0600"
          owner: root:root
          content: |
            systemReserved:
              cpu: "{{ $r.systemReservedCpu }}"
              memory: "{{ $r.systemReservedMemory }}"
            kubeReserved:
              cpu: "{{ $r.kubeReservedCpu }}"
              memory: "{{ $r.kubeReservedMemory }}"
            evictionHard:
              memory.available: "{{ $r.evictionHardMemory }}"
              nodefs.available: "10%"
              imagefs.available: "15%"
              nodefs.inodesFree: "5%"
            evictionSoft:
              memory.available: "{{ $r.evictionSoftMemory }}"
              nodefs.available: "15%"
              imagefs.available: "20%"
            evictionSoftGracePeriod:
              memory.available: "1m30s"
              nodefs.available: "1m30s"
              imagefs.available: "1m30s"
            evictionMinimumReclaim:
              memory.available: "256Mi"
      kubeadm:
        joinConfiguration:
          patches:
            directory: /etc/kubernetes/kairos-patches
{{- end }}

{{- define "kubernetes.kairosConfigTemplateHash" -}}
{{- include "kubernetes.kairosConfigTemplateSpec" . | sha256sum | trunc 6 -}}
{{- end }}

{{- /* The worker disk a `kairos` pool boots: kairos.image when set, otherwise
       the platform disk built for the pool's Kubernetes minor. */}}
{{- define "kubernetes-nodes.kairosImage" -}}
{{- $image := .Values.kairos.image | default (.Files.Get (printf "images/kairos-worker-%s.tag" .Values.version) | trim) -}}
{{- if not $image -}}
{{- fail (printf "os kairos: this platform ships no worker disk for Kubernetes %s; set kairos.image" .Values.version) -}}
{{- end -}}
{{- $image -}}
{{- end }}
