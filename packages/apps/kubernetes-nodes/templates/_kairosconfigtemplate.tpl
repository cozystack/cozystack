{{- /*
  Runtime Service addresses stay out of the template hash: hashing them
  would replace workers on a Service re-address. The two addresses are the
  only placeholders the Job fills in, under a reserved marker no tenant value
  may carry; every other value, the release and namespace included, is
  rendered here. Tenant inputs are YAML data, not shell expressions.

  The worker joins https://<release>.<ns>.svc:6443 by name. kubeadm also
  reads the server name from the cluster-info ConfigMap the control plane
  publishes, so the name has to resolve during the join itself. On the first
  boot the files below are written after systemd-resolved has started, and a
  resolved drop-in takes effect only from the next boot; /etc/hosts is read
  on every lookup, so the apiserver entry there is what makes the first join
  work.
*/}}
{{- define "kubernetes.kairosConfigTemplateSpec" -}}
{{- $root := .root }}
{{- $group := .group }}
{{- $kubeletVersion := include "kubernetes-nodes.kubernetesVersion" (dict "root" $root "class" .class) }}
{{- $dnsDomain := include "kubernetes.tenantClusterDomain" $root }}
{{- $apiserver := printf "%s.%s.svc" (include "kubernetes-nodes.clusterName" $root) $root.Release.Namespace }}
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
      serverAddress: "https://{{ $apiserver }}:6443"
      userName: kairos
      userGroups:
        - admin
      {{- /* The provider refuses a worker without a credential. "*" goes
             into /etc/shadow as is and matches no password, so there is no
             password login; unlike "!", sshd does not treat it as a locked
             account, so sshPublicKey still works. */}}
      userPassword: "*"
      {{- with $root.Values.sshKeys }}
      sshPublicKey: {{ first . | quote }}
      {{- end }}
      files:
        - path: /etc/hosts
          permissions: "0644"
          owner: root:root
          content: |
            127.0.0.1 localhost
            ::1 localhost ip6-localhost ip6-loopback
            __COZY_RUNTIME_SVC_IP__ {{ $apiserver }} {{ $apiserver }}.{{ $dnsDomain }}
        - path: /etc/systemd/resolved.conf.d/cozystack.conf
          permissions: "0644"
          owner: root:root
          content: |
            [Resolve]
            DNS=__COZY_RUNTIME_COREDNS_IP__
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

{{- define "kubernetes-nodes.kairosImage" -}}
{{- $image := "" -}}
{{- with .class.image -}}
{{- $image = .reference -}}
{{- else -}}
{{- $image = $.root.Files.Get (printf "images/kairos-worker-%s.tag" $.root.Values.version) | trim -}}
{{- if not $image -}}
{{- fail (printf "WorkerClass %q: this platform ships no Kairos worker disk for Kubernetes %s" $.class.name $.root.Values.version) -}}
{{- end -}}
{{- $image = include "cozy-lib.image" (list $image $.root) -}}
{{- end -}}
{{- $image -}}
{{- end }}
