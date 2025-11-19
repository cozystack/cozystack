package factory

import (
	"time"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

func BackupRun(p *backupsv1alpha1.Plan, scheduledFor time.Time) *backupsv1alpha1.BackupRun {
	return nil
}
