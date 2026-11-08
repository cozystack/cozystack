// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
	"github.com/cozystack/cozystack/internal/backupcontroller/mariadbtypes"
)

const (
	mariadbSkipArtifactCleanupAnnotation = "backups.cozystack.io/skip-artifact-cleanup"

	// Retry cadence while a dump cannot be deleted or a restore from the
	// Backup is still running.
	mariadbCleanupRetryInterval = 30 * time.Second
)

// mariadbDumpCleanupTimeout bounds the object-store round trips of one
// cleanup attempt. The Backup controller has a single worker, so an
// unreachable endpoint stalls every other Backup for this long. A variable
// so tests can shorten it.
var mariadbDumpCleanupTimeout = 10 * time.Second

// cleanupMariaDBBackup deletes the dump a MariaDB Backup was taken as, then
// the operator Backup that wrote it. Every run of an application writes
// under one prefix, so the only key deleted is one proven to be this
// Backup's: the dump it records, or, for a Backup taken before dumps were
// recorded, the one findMariaDBDump attributes to its operator Backup. When
// no dump is proven the Backup is released with a Warning and nothing is
// deleted, the operator Backup included, since it is what any later
// attribution would start from. A failed delete holds the Backup and
// retries, as the Redis and Rabbitmq cleanups do.
func (r *BackupReconciler) cleanupMariaDBBackup(ctx context.Context, backup *backupsv1alpha1.Backup) (ctrl.Result, error) {
	logger := getLogger(ctx)
	if backup.Annotations[mariadbSkipArtifactCleanupAnnotation] == "true" {
		logger.Debug("skipping MariaDB artifact cleanup per annotation", "backup", backup.Name, "annotation", mariadbSkipArtifactCleanupAnnotation)
		return ctrl.Result{}, nil
	}

	opName := backup.Spec.DriverMetadata[mariadbBackupNameKey]
	if opName == "" {
		return r.releaseMariaDBCleanup(ctx, backup, "the dump", "the Backup records no k8s.mariadb.com/Backup"), nil
	}

	// The operator Restore reads the dump through the operator Backup, so
	// neither may go while a restore from this Backup runs.
	restore, err := r.activeRestoreOf(ctx, backup)
	if err != nil {
		return ctrl.Result{}, err
	}
	if restore != "" {
		logger.Debug("holding MariaDB Backup cleanup while a restore from it runs", "backup", backup.Name, "restorejob", restore)
		if r.Recorder != nil {
			r.Recorder.Eventf(backup, corev1.EventTypeNormal, "ArtifactCleanupWaiting",
				"RestoreJob %s is restoring from this Backup; its dump is deleted once the restore finishes. Annotate the Backup %s=true to release it now and leave the dump",
				restore, mariadbSkipArtifactCleanupAnnotation)
		}
		return ctrl.Result{RequeueAfter: mariadbCleanupRetryInterval}, nil
	}

	op := &mariadbtypes.Backup{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: backup.Namespace, Name: opName}, op); err != nil {
		if apierrors.IsNotFound(err) || apimeta.IsNoMatchError(err) {
			return r.releaseMariaDBCleanup(ctx, backup, "the dump",
				fmt.Sprintf("k8s.mariadb.com/Backup %s is gone, so the storage and credentials its dump was written with cannot be read", opName)), nil
		}
		return ctrl.Result{}, err
	}

	s3 := op.Spec.Storage.S3
	if s3 == nil {
		return r.releaseMariaDBCleanup(ctx, backup, fmt.Sprintf("the dump on the storage volume of k8s.mariadb.com/Backup %s", opName),
			"PVC and Volume storage is not reachable from the controller"), nil
	}
	prefix := mariadbOperatorS3Prefix(s3.Prefix)
	location := fmt.Sprintf("s3://%s/%s", s3.Bucket, prefix)

	// A failed Job is Complete=True too, with reason JobFailed, and wrote no
	// dump: whatever its window holds is another run's.
	if cond := apimeta.FindStatusCondition(op.Status.Conditions, mariadbtypes.ConditionTypeComplete); cond == nil ||
		cond.Status != metav1.ConditionTrue || cond.Reason != mariadbtypes.ConditionReasonJobComplete {
		return r.releaseMariaDBCleanup(ctx, backup, location,
			fmt.Sprintf("k8s.mariadb.com/Backup %s did not complete successfully, so no dump is its own", opName)), nil
	}

	name := backup.Spec.DriverMetadata[mariadbDumpFileKey]
	if name != "" {
		if _, ok := parseMariaDBLogicalBackupFile(name); !ok {
			return r.releaseMariaDBCleanup(ctx, backup, location,
				fmt.Sprintf("driverMetadata %s=%q is not the name of a mariadb-operator dump", mariadbDumpFileKey, name)), nil
		}
	}

	conn, err := r.mariadbCleanupConnection(ctx, op)
	if err != nil {
		return r.retryMariaDBCleanup(ctx, backup, location, fmt.Errorf("resolve S3 credentials: %w", err))
	}

	s3ctx, cancel := context.WithTimeout(ctx, mariadbDumpCleanupTimeout)
	defer cancel()
	store := r.objectStore()
	if name == "" {
		lister, err := store.lister(conn)
		if err != nil {
			return r.retryMariaDBCleanup(ctx, backup, location, fmt.Errorf("build S3 client: %w", err))
		}
		dump, err := findMariaDBDumpWith(s3ctx, lister, r.Client, op, backup.Spec.TakenAt.Time, true)
		if errors.Is(err, errMariaDBDumpNotFound) || errors.Is(err, errMariaDBDumpAmbiguous) {
			return r.releaseMariaDBCleanup(ctx, backup, location,
				fmt.Sprintf("the Backup records no dump and none is proven to be the one k8s.mariadb.com/Backup %s wrote (%v); the operator Backup is kept", opName, err)), nil
		}
		if err != nil {
			return r.retryMariaDBCleanup(ctx, backup, location, err)
		}
		name = dump.Name
		// Recorded first: once the dump is gone a retry could not match it
		// again, and could match a neighbour's instead.
		base := backup.DeepCopy()
		backup.Spec.DriverMetadata[mariadbDumpFileKey] = name
		backup.Spec.DriverMetadata[mariadbDumpTimeKey] = dump.Timestamp.UTC().Format(time.RFC3339)
		if err := r.Patch(ctx, backup, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, fmt.Errorf("record dump %s on Backup %s: %w", name, backup.Name, err)
		}
	}

	remover, err := store.remover(conn)
	if err != nil {
		return r.retryMariaDBCleanup(ctx, backup, location, fmt.Errorf("build S3 client: %w", err))
	}
	if err := remover.RemoveObject(s3ctx, s3.Bucket, prefix+name); err != nil {
		return r.retryMariaDBCleanup(ctx, backup, location+name, err)
	}
	policy := metav1.DeletePropagationBackground
	if err := r.Delete(ctx, op, &client.DeleteOptions{PropagationPolicy: &policy}); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("delete k8s.mariadb.com/Backup %s/%s: %w", op.Namespace, op.Name, err)
	}
	logger.Info("deleted MariaDB dump and its operator Backup", "backup", backup.Name, "dump", location+name, "operatorBackup", opName)
	return ctrl.Result{}, nil
}

// activeRestoreOf returns the name of a RestoreJob that restores from
// backup and has not finished, or "".
func (r *BackupReconciler) activeRestoreOf(ctx context.Context, backup *backupsv1alpha1.Backup) (string, error) {
	list := &backupsv1alpha1.RestoreJobList{}
	if err := r.List(ctx, list, client.InNamespace(backup.Namespace)); err != nil {
		return "", fmt.Errorf("list RestoreJobs: %w", err)
	}
	for i := range list.Items {
		rj := &list.Items[i]
		if rj.Spec.BackupRef.Name != backup.Name {
			continue
		}
		if rj.Status.Phase != backupsv1alpha1.RestoreJobPhaseSucceeded && rj.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
			return rj.Name, nil
		}
	}
	return "", nil
}

// mariadbCleanupConnection returns how to reach the bucket op wrote to. A
// dump on the platform bucket, which op names by its bucket and by the
// Secret the platform credentials are projected to, is reached as the
// platform reaches it now, so a Backup taken before the endpoint moved is
// still deleted. Any other bucket, or the platform bucket while the
// platform credentials cannot be read, is reached as op recorded it; a
// delete that fails there is handled by retryMariaDBCleanup like any other.
func (r *BackupReconciler) mariadbCleanupConnection(ctx context.Context, op *mariadbtypes.Backup) (s3Connection, error) {
	if r.CredentialsConfig.IsEnabled() {
		conn, bucket, err := r.platformS3Connection(ctx)
		switch {
		case err != nil:
			getLogger(ctx).Info("cannot read the platform backup credentials; using the operator Backup's own", "operatorBackup", op.Name, "error", err)
		case bucket == op.Spec.Storage.S3.Bucket && op.Spec.Storage.S3.AccessKeyIdSecretKeyRef.Name == r.CredentialsConfig.TargetSecretName:
			// A strategy can pin a private CA for the platform bucket; the
			// platform credentials carry none.
			if tls := op.Spec.Storage.S3.TLS; conn.Secure && tls != nil && tls.CASecretKeyRef != nil {
				ca, err := r.objectStore().secretValue(ctx, op.Namespace, tls.CASecretKeyRef.Name, tls.CASecretKeyRef.Key)
				if err != nil {
					return s3Connection{}, err
				}
				conn.CAPEM = []byte(ca)
			}
			return conn, nil
		}
	}
	return mariadbS3Connection(ctx, r.objectStore(), op.Namespace, op.Spec.Storage.S3)
}

// platformS3Connection reads the platform's backup bucket from the Secret
// cozy-backups-creds is projected from, and its TLS from the scheme of
// BACKUP_STORAGE_ENDPOINT, which is how the default strategy sets
// tls.enabled. It reads the source rather than projecting it: projecting
// writes a Secret, which a terminating namespace refuses.
func (r *BackupReconciler) platformS3Connection(ctx context.Context) (s3Connection, string, error) {
	cfg := r.CredentialsConfig
	src := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: cfg.SourceNamespace, Name: cfg.SourceSecretName}, src); err != nil {
		if apierrors.IsNotFound(err) {
			return s3Connection{}, "", &ProjectionError{Reason: ReasonSourceMissing,
				Message: fmt.Sprintf("source credentials Secret %s/%s not found", cfg.SourceNamespace, cfg.SourceSecretName)}
		}
		return s3Connection{}, "", &ProjectionError{Reason: ReasonAPIError, Message: fmt.Sprintf("read source credentials Secret: %v", err)}
	}
	creds, err := parseSourceSecret(src)
	if err != nil {
		return s3Connection{}, "", err
	}
	if creds.endpoint == "" {
		creds.endpoint = stripScheme(cfg.Endpoint)
	}
	if creds.endpoint == "" {
		return s3Connection{}, "", &ProjectionError{Reason: ReasonSourceMalformed,
			Message: fmt.Sprintf("endpoint is empty in both source Secret %s/%s and BACKUP_STORAGE_ENDPOINT", cfg.SourceNamespace, cfg.SourceSecretName)}
	}
	if creds.region == "" {
		creds.region = cfg.Region
	}
	return s3Connection{
		Endpoint:  creds.endpoint,
		Secure:    strings.HasPrefix(cfg.Endpoint, "https://"),
		Region:    creds.region,
		AccessKey: creds.accessKey,
		SecretKey: creds.secretKey,
	}, creds.bucket, nil
}

// retryMariaDBCleanup holds the Backup after a failed attempt and says so,
// unless its namespace is going away, where nothing it needs can be read
// any longer and holding it would hold the namespace.
func (r *BackupReconciler) retryMariaDBCleanup(ctx context.Context, backup *backupsv1alpha1.Backup, location string, cause error) (ctrl.Result, error) {
	terminating, err := r.namespaceTerminating(ctx, backup.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if terminating {
		return r.releaseMariaDBCleanup(ctx, backup, location, fmt.Sprintf("namespace is terminating and the dump cannot be deleted: %v", cause)), nil
	}
	getLogger(ctx).Info("cannot delete MariaDB dump; retrying", "backup", backup.Name, "location", location, "error", cause)
	if r.Recorder != nil {
		r.Recorder.Eventf(backup, corev1.EventTypeWarning, "ArtifactCleanupFailed",
			"cannot delete the dump under %s: %v; retrying. Annotate the Backup %s=true to release it and leave the dump",
			location, cause, mariadbSkipArtifactCleanupAnnotation)
	}
	return ctrl.Result{RequeueAfter: mariadbCleanupRetryInterval}, nil
}

// releaseMariaDBCleanup lets the Backup go without deleting anything further
// and records a Warning naming what was left, so the give-up is visible.
func (r *BackupReconciler) releaseMariaDBCleanup(ctx context.Context, backup *backupsv1alpha1.Backup, location, reason string) ctrl.Result {
	getLogger(ctx).Info("releasing Backup without deleting its dump", "backup", backup.Name, "location", location, "reason", reason)
	if r.Recorder != nil {
		r.Recorder.Eventf(backup, corev1.EventTypeWarning, "ArtifactNotDeleted", "left %s behind: %s", location, reason)
	}
	return ctrl.Result{}
}
