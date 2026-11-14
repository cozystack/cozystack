{{/*
Whether the cluster serves the VolumeGroupSnapshot API that csi-snapshotter and
Velero use. snapshot-controller can still serve v1alpha1 while this release
upgrades: migration 61 keeps a group snapshot CRD that holds objects, and the
snapshot-controller release stays Ready on its old revision until it upgrades.
With the gate on and the API missing, csi-snapshotter waits for its group
snapshot caches before it starts the ordinary snapshot workers.
*/}}
{{- define "linstor.groupSnapshotAPIServed" -}}
{{- if and (.Capabilities.APIVersions.Has "groupsnapshot.storage.k8s.io/v1beta2/VolumeGroupSnapshot") (.Capabilities.APIVersions.Has "groupsnapshot.storage.k8s.io/v1beta2/VolumeGroupSnapshotContent") (.Capabilities.APIVersions.Has "groupsnapshot.storage.k8s.io/v1beta2/VolumeGroupSnapshotClass") -}}
true
{{- end -}}
{{- end -}}
