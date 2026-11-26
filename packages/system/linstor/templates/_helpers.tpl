{{/*
Whether the cluster serves the groupsnapshot.storage.k8s.io/v1 API that
csi-snapshotter and snapshot-controller read. snapshot-controller can still
serve v1alpha1 while this release upgrades: migration 62 keeps a group snapshot
CRD that holds objects, and the snapshot-controller release stays Ready on its
old revision until it upgrades. Leaving the gate off until then, rather than
always on, makes the first linstor upgrade that sees the API roll the
csi-controller: csi-snapshotter looks for the API only when it starts.
*/}}
{{- define "linstor.groupSnapshotAPIServed" -}}
{{- if and (.Capabilities.APIVersions.Has "groupsnapshot.storage.k8s.io/v1/VolumeGroupSnapshot") (.Capabilities.APIVersions.Has "groupsnapshot.storage.k8s.io/v1/VolumeGroupSnapshotContent") (.Capabilities.APIVersions.Has "groupsnapshot.storage.k8s.io/v1/VolumeGroupSnapshotClass") -}}
true
{{- end -}}
{{- end -}}
