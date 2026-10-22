// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
)

// cnpgArtifactSizeBytes sums the objects barman-cloud uploaded for one base
// backup, <destinationPath>/<serverName>/base/<backupID>/. The WAL archive
// is not counted: it is one continuous stream shared by every base backup of
// the server, so no part of it belongs to a single Backup. Returns 0 -
// leaving Artifact.SizeBytes absent - when the backup ID is unknown, the
// bucket cannot be read, or nothing is stored under the prefix.
func (r *BackupJobReconciler) cnpgArtifactSizeBytes(
	ctx context.Context,
	namespace string,
	store *strategyv1alpha1.BarmanObjectStoreTemplate,
	serverName, backupID string,
) int64 {
	logger := getLogger(ctx)
	if backupID == "" {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, artifactSizeProbeTimeout)
	defer cancel()

	bucket, path, err := parseBarmanS3DestinationPath(store.DestinationPath)
	if err != nil {
		logger.Info("cannot measure CNPG backup size", "backupID", backupID, "error", err)
		return 0
	}
	conn, err := r.cnpgS3Connection(ctx, namespace, store)
	if err != nil {
		logger.Info("cannot resolve S3 credentials to measure CNPG backup size", "backupID", backupID, "error", err)
		return 0
	}
	lister, err := r.s3Lister(conn)
	if err != nil {
		logger.Info("cannot build S3 client to measure CNPG backup size", "backupID", backupID, "error", err)
		return 0
	}
	prefix := barmanBaseBackupPrefix(path, serverName, backupID)
	objects, err := lister.ListObjects(ctx, bucket, prefix, true)
	if err != nil {
		logger.Info("cannot list CNPG base backup objects to measure size", "backupID", backupID, "error", err)
		return 0
	}
	var total int64
	for _, obj := range objects {
		total += obj.Size
	}
	if total == 0 {
		logger.Info("no CNPG base backup objects found; leaving size unset", "backupID", backupID, "prefix", prefix)
	}
	return total
}

func (r *BackupJobReconciler) cnpgS3Connection(ctx context.Context, namespace string, store *strategyv1alpha1.BarmanObjectStoreTemplate) (s3Connection, error) {
	var conn s3Connection
	if store.S3Credentials == nil {
		return conn, errors.New("barmanObjectStore has no s3Credentials")
	}
	if store.EndpointURL == "" {
		conn.Endpoint, conn.Secure = "s3.amazonaws.com", true
	} else {
		u, err := url.Parse(store.EndpointURL)
		if err != nil || u.Host == "" {
			return conn, fmt.Errorf("invalid endpointURL %q", store.EndpointURL)
		}
		// Dropping the path would probe the bucket at another location than
		// the one barman writes to.
		if strings.Trim(u.Path, "/") != "" {
			return conn, fmt.Errorf("endpointURL %q has a path, which the S3 client cannot address", store.EndpointURL)
		}
		conn.Endpoint, conn.Secure = u.Host, u.Scheme == "https"
	}

	creds := store.S3Credentials
	accessKeyKey, secretKeyKey := creds.AccessKeyIDKey, creds.SecretAccessKeyKey
	if accessKeyKey == "" {
		accessKeyKey = defaultS3AccessKeyIDKey
	}
	if secretKeyKey == "" {
		secretKeyKey = defaultS3SecretAccessKeyKey
	}
	var err error
	if conn.AccessKey, err = r.secretValue(ctx, namespace, creds.SecretRef.Name, accessKeyKey); err != nil {
		return conn, err
	}
	if conn.SecretKey, err = r.secretValue(ctx, namespace, creds.SecretRef.Name, secretKeyKey); err != nil {
		return conn, err
	}
	if ca := store.EndpointCA; ca != nil && ca.SecretRef.Name != "" && conn.Secure {
		key := ca.Key
		if key == "" {
			key = defaultEndpointCAKey
		}
		pem, err := r.secretValue(ctx, namespace, ca.SecretRef.Name, key)
		if err != nil {
			return conn, err
		}
		// barman-cloud gets endpointCA as AWS_CA_BUNDLE, which replaces the
		// default trust store rather than adding to it.
		conn.CAPEM, conn.CAOnly = []byte(pem), true
	}
	return conn, nil
}

// parseBarmanS3DestinationPath splits s3://bucket/path into the bucket and
// the key path barman-cloud prefixes every object with.
func parseBarmanS3DestinationPath(destinationPath string) (bucket, path string, err error) {
	u, err := url.Parse(destinationPath)
	if err != nil || u.Scheme != "s3" || u.Host == "" {
		return "", "", fmt.Errorf("destinationPath %q is not an s3:// URL", destinationPath)
	}
	return u.Host, strings.Trim(u.Path, "/"), nil
}

// barmanBaseBackupPrefix mirrors barman-cloud's
// os.path.join(path, server_name, "base", backup_id), with a trailing slash
// so one backup ID never matches another that extends it.
func barmanBaseBackupPrefix(path, serverName, backupID string) string {
	parts := make([]string, 0, 4)
	if path != "" {
		parts = append(parts, path)
	}
	parts = append(parts, serverName, "base", backupID)
	return strings.Join(parts, "/") + "/"
}
