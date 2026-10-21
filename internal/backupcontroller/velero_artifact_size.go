// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"

	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// veleroArtifactSizeBytes sums status.progress.totalBytes over the
// DataUploads the Velero data mover ran for veleroBackup: the logical size
// of the backed-up volumes, i.e. the bytes read from them and written back
// on restore. It is not the space the backup takes in the kopia repository,
// which compresses and deduplicates data across backups, so no per-backup
// stored size exists.
//
// Returns 0 - leaving Artifact.SizeBytes absent - when the backup moved no
// volume data, when any of its DataUploads did not complete with a total,
// or when it also took native provider snapshots, whose bytes no DataUpload
// reports: a partial sum must not be reported as the backup's size.
func (r *BackupJobReconciler) veleroArtifactSizeBytes(ctx context.Context, veleroBackup *velerov1.Backup) int64 {
	logger := getLogger(ctx)
	if veleroBackup.UID == "" || veleroBackup.Status.VolumeSnapshotsAttempted > 0 {
		return 0
	}

	ul := unstructured.UnstructuredList{}
	ul.SetGroupVersionKind(dataUploadListGVK)
	if err := r.List(ctx, &ul,
		client.InNamespace(veleroBackup.Namespace),
		client.MatchingLabels{velerov1.BackupUIDLabel: string(veleroBackup.UID)},
	); err != nil {
		logger.Info("cannot list DataUploads to measure Velero backup size", "backup", veleroBackup.Name, "error", err)
		return 0
	}

	var total int64
	for _, item := range ul.Items {
		phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
		bytes, _, _ := unstructured.NestedInt64(item.Object, "status", "progress", "totalBytes")
		if phase != "Completed" || bytes <= 0 {
			logger.Info("DataUpload has no completed total; leaving Velero backup size unset",
				"backup", veleroBackup.Name, "dataUpload", item.GetName(), "phase", phase)
			return 0
		}
		total += bytes
	}
	return total
}
