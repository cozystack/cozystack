{{- define "capi-providers-bootstrap.assertUpgradeWindow" -}}
{{- if .Capabilities.APIVersions.Has "operator.cluster.x-k8s.io/v1alpha2" -}}
{{- $provider := lookup "operator.cluster.x-k8s.io/v1alpha2" "BootstrapProvider" .Release.Namespace "kubeadm" -}}
{{- if $provider -}}
{{- $status := get $provider "status" | default dict -}}
{{- $installed := get $status "installedVersion" -}}
{{- if or (not (kindIs "string" $installed)) (eq $installed "") -}}
{{- fail "kubeadm provider installed version is not recorded; wait for the existing installation before upgrading" -}}
{{- end -}}
{{- $current := semver $installed -}}
{{- $target := semver .Chart.AppVersion -}}
{{- if or (gt $current.Major $target.Major) (and (eq $current.Major $target.Major) (gt $current.Minor $target.Minor)) (and (eq $current.Major $target.Major) (eq $current.Minor $target.Minor) (gt $current.Patch $target.Patch)) -}}
{{- fail "Cluster API provider downgrades are not supported" -}}
{{- end -}}
{{- if or (ne $current.Major $target.Major) (gt (sub $target.Minor $current.Minor) 3) -}}
{{- fail "kubeadm provider cannot skip more than three minor versions; upgrade the core provider package first to run the intermediate migration" -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
