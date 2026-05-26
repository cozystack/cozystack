package backupcontroller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// CredentialsProjectionRequeue is the backoff between reconcile attempts
// when the source credentials Secret hasn't propagated yet. Short enough
// that the first BackupJob after a fresh install does not stall, long
// enough that a permanently mis-configured source does not flood the
// controller. Shared between BackupJob and RestoreJob reconcilers so a
// change in one half cannot drift from the other.
const CredentialsProjectionRequeue = 30 * time.Second

// managedByLabel marks Secrets the credentials projector owns. The
// projector refuses to update any pre-existing Secret that lacks this
// label so a tenant cannot accidentally have a manually-created or
// restored cozy-backups-creds clobbered on the next BackupJob reconcile.
const (
	managedByLabel = "apps.cozystack.io/managed-by"
	managedByValue = "cozystack-backups"
)

// ProjectionError is a typed error so callers (reconcilers) can decide
// whether projection failure is transient (source not yet populated by
// the bucket controller) or terminal (target Secret owned by someone
// else and refuses to be overwritten).
type ProjectionError struct {
	Reason  string
	Message string
}

func (e *ProjectionError) Error() string { return e.Message }

// Reason identifiers carried on ProjectionError. Callers compare against
// these to drive Condition reasons / metrics labels.
const (
	ReasonSourceMissing   = "SourceSecretMissing"
	ReasonSourceMalformed = "SourceSecretMalformed"
	ReasonTargetNotOwned  = "TargetSecretNotOwned"
	ReasonAPIError        = "APIError"
)

// IsTransient reports whether a projection error is expected to clear on
// its own (source Secret not yet propagated, apiserver hiccup) or whether
// it surfaces an operator-visible misconfiguration that needs a human.
// Callers use this to decide between requeue and terminal Failed.
func IsTransient(err error) bool {
	var perr *ProjectionError
	if !errors.As(err, &perr) {
		return false
	}
	switch perr.Reason {
	case ReasonSourceMissing, ReasonAPIError:
		return true
	}
	return false
}

// BackupCredentialsConfig describes the platform-managed source Secret
// produced by the cozy-backups Bucket, the per-tenant projection target
// Secret name, and the bucket coordinates (endpoint used to format the
// FDB blob_credentials account-host key, region carried verbatim for
// drivers like clickhouse-backup that wire it through env). Values are
// wired from environment variables in main() and the deployment chart
// populates them from .Values.backupStorage.
type BackupCredentialsConfig struct {
	SourceNamespace  string
	SourceSecretName string
	TargetSecretName string
	Endpoint         string
	Region           string
}

// IsEnabled reports whether credentials projection is configured. When
// disabled (any required field empty), reconcilers skip projection —
// strategies that need cozy-backups-creds then fail validation downstream,
// which is the intended behaviour on clusters still on the legacy
// chart-managed flow.
func (c BackupCredentialsConfig) IsEnabled() bool {
	return c.SourceNamespace != "" && c.SourceSecretName != "" && c.TargetSecretName != ""
}

// ProjectBackupCredentials copies the platform-managed S3 credentials Secret
// from the configured source namespace to a tenant namespace, formatting
// the data so every default Strategy CR can consume the same Secret:
//
//   - AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY — CNPG, MariaDB, Etcd.
//   - accessKey / secretKey + bucketName / endpoint / region — ClickHouse
//     sidecar.
//   - cloud — Velero credentials file ([default] section).
//   - blob_credentials.json — FoundationDB backup_agent.
//
// The source Secret is read in two formats:
//
//  1. Human-friendly flat keys (accessKey/secretKey/endpoint/bucketName),
//     produced by packages/system/bucket/templates/user-credentials.yaml.
//  2. Raw COSI Secret with a single "BucketInfo" JSON blob, produced by
//     the COSI driver before the user-credentials renderer runs (or when
//     the cluster wires an external S3 Secret manually). Parsed best-
//     effort; cluster admins can override by writing flat keys.
//
// The projection is idempotent and safe to invoke on every reconcile
// pass. Pre-existing target Secrets without the managed-by label are
// refused — see managedByLabel.
//
// SECURITY: the projected Secret deliberately omits the
// internal.cozystack.io/tenantresource label, so the lineage-controller
// webhook does NOT promote it to a TenantSecret view — tenants without
// core/v1.Secret verbs (default cozy:tenant:base) cannot read the keys.
// Pods of operators that consume the Secret mount it via kubelet, which
// bypasses tenant RBAC.
func ProjectBackupCredentials(ctx context.Context, c client.Client, cfg BackupCredentialsConfig, targetNamespace string) error {
	if !cfg.IsEnabled() {
		return nil
	}
	src := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: cfg.SourceNamespace, Name: cfg.SourceSecretName}, src); err != nil {
		if apierrors.IsNotFound(err) {
			return &ProjectionError{
				Reason:  ReasonSourceMissing,
				Message: fmt.Sprintf("source credentials Secret %s/%s not found", cfg.SourceNamespace, cfg.SourceSecretName),
			}
		}
		return &ProjectionError{
			Reason:  ReasonAPIError,
			Message: fmt.Sprintf("read source credentials Secret: %v", err),
		}
	}

	creds, err := parseSourceSecret(src)
	if err != nil {
		return err
	}
	if creds.endpoint == "" {
		creds.endpoint = cfg.Endpoint
	}
	if creds.endpoint == "" {
		// Both source Secret and BACKUP_STORAGE_ENDPOINT are empty. Fail
		// loud rather than projecting a half-broken Secret (Velero's BSL
		// s3Url="" silently rejects Backups, FDB blob_credentials cannot
		// be rendered without a host). Surfaced as terminal misconfig
		// so the admin gets a clear signal at first BackupJob instead of
		// debugging downstream "object not found" errors.
		return &ProjectionError{
			Reason:  ReasonSourceMalformed,
			Message: fmt.Sprintf("endpoint is empty in both source Secret %s/%s and BACKUP_STORAGE_ENDPOINT", cfg.SourceNamespace, cfg.SourceSecretName),
		}
	}
	if creds.region == "" {
		creds.region = cfg.Region
	}

	cloud := buildVeleroCredentialsFile(creds.accessKey, creds.secretKey)
	blob, err := buildFDBBlobCredentials(creds.endpoint, creds.accessKey, creds.secretKey)
	if err != nil {
		return &ProjectionError{
			Reason:  ReasonSourceMalformed,
			Message: fmt.Sprintf("build blob_credentials.json: %v", err),
		}
	}

	// Ownership guard lives INSIDE the CreateOrUpdate mutator (rather
	// than in a separate Get preflight) for fewer round trips: the
	// mutator runs against the same object CreateOrUpdate just
	// re-fetched, and returning an error from the closure short-circuits
	// the Update. There is still a small window where a racing actor's
	// Create-of-an-unlabelled-Secret can land between CreateOrUpdate's
	// internal Get and the Create call — in that case the apiserver
	// returns AlreadyExists, controller-runtime surfaces the error
	// without re-invoking the mutator, the projection fails this round,
	// and the next reconcile cycle sees the existing unlabelled Secret
	// and hits the guard. Safe modulo one extra apiserver round trip on
	// concurrent racing Create; no stomp.
	//
	// Surfaced error: when the closure returns a ProjectionError, the
	// outer CreateOrUpdate wraps it as a plain error — IsTransient unwraps
	// via errors.As, so callers still classify ReasonTargetNotOwned as
	// terminal.
	target := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: targetNamespace,
			Name:      cfg.TargetSecretName,
		},
	}
	var ownershipErr *ProjectionError
	if _, err := controllerutil.CreateOrUpdate(ctx, c, target, func() error {
		// On Update path, target has been re-fetched from the cluster
		// (ResourceVersion populated). On Create path, ResourceVersion is
		// empty — there is no existing object to own.
		isExisting := target.ResourceVersion != ""
		if isExisting && target.Labels[managedByLabel] != managedByValue {
			ownershipErr = &ProjectionError{
				Reason: ReasonTargetNotOwned,
				Message: fmt.Sprintf(
					"target Secret %s/%s exists without label %s=%s; refusing to overwrite",
					targetNamespace, cfg.TargetSecretName, managedByLabel, managedByValue),
			}
			return ownershipErr
		}
		if target.Labels == nil {
			target.Labels = map[string]string{}
		}
		target.Labels[managedByLabel] = managedByValue
		target.Type = corev1.SecretTypeOpaque
		if target.Data == nil {
			target.Data = map[string][]byte{}
		}
		target.Data["AWS_ACCESS_KEY_ID"] = []byte(creds.accessKey)
		target.Data["AWS_SECRET_ACCESS_KEY"] = []byte(creds.secretKey)
		// Mirror under the legacy bucket-controller key names so
		// chart-emitted sidecars (clickhouse-backup) can consume the
		// same Secret without a separate field map.
		target.Data["accessKey"] = []byte(creds.accessKey)
		target.Data["secretKey"] = []byte(creds.secretKey)
		target.Data["cloud"] = []byte(cloud)
		target.Data["blob_credentials.json"] = blob
		// Always overwrite endpoint/bucket/region (delete if empty in
		// source) so a re-projection cannot leave stale values lingering
		// after the bucket-controller rotates the source. Consumers
		// (ClickHouse sidecar via secretKeyRef) must see exactly what the
		// source carries, not a half-stale Secret.
		setOrDelete(target.Data, "endpoint", creds.endpoint)
		setOrDelete(target.Data, "bucketName", creds.bucket)
		setOrDelete(target.Data, "region", creds.region)
		return nil
	}); err != nil {
		// If the mutator refused to overwrite an unowned Secret, surface
		// the typed ProjectionError directly (callers route on Reason).
		if ownershipErr != nil {
			return ownershipErr
		}
		return &ProjectionError{
			Reason:  ReasonAPIError,
			Message: fmt.Sprintf("project credentials Secret to %s/%s: %v", targetNamespace, cfg.TargetSecretName, err),
		}
	}
	return nil
}

// setOrDelete writes value under key when value is non-empty, otherwise
// deletes the key from the map. Used to keep the projected Secret
// strictly in sync with the source — a re-projection after the source
// loses a field must not leave the previous value behind.
func setOrDelete(m map[string][]byte, key, value string) {
	if value == "" {
		delete(m, key)
		return
	}
	m[key] = []byte(value)
}

// parsedCreds is the in-memory shape both source formats are normalised
// into before the projector renders the per-driver representations.
type parsedCreds struct {
	accessKey string
	secretKey string
	endpoint  string
	bucket    string
	region    string
}

// parseSourceSecret accepts either the human-friendly flat-key Secret
// produced by packages/system/bucket/templates/user-credentials.yaml
// (accessKey/secretKey/endpoint/bucketName) or the raw COSI Secret
// containing a single BucketInfo JSON document, and returns the
// normalised credentials. Per-field fallback: each missing flat key
// falls back to the matching BucketInfo field when both are present,
// rather than gating the whole JSON parse on accessKey/secretKey being
// absent. The endpoint is stripped of any scheme prefix to match the
// host:port shape downstream callers expect.
func parseSourceSecret(src *corev1.Secret) (parsedCreds, error) {
	out := parsedCreds{
		accessKey: string(src.Data["accessKey"]),
		secretKey: string(src.Data["secretKey"]),
		endpoint:  string(src.Data["endpoint"]),
		bucket:    string(src.Data["bucketName"]),
		region:    string(src.Data["region"]),
	}

	// COSI fallback: BucketInfo JSON blob. Always consulted (when
	// present) so a flat-key Secret that omits bucket/endpoint/region
	// can still pull those from the raw COSI document — the flat-key
	// renderer in packages/system/bucket strips region for example, and
	// callers should not have to know which fields are populated.
	if raw, ok := src.Data["BucketInfo"]; ok && len(raw) > 0 {
		var info struct {
			Spec struct {
				BucketName string `json:"bucketName"`
				SecretS3   struct {
					AccessKeyID     string `json:"accessKeyID"`
					AccessSecretKey string `json:"accessSecretKey"`
					Endpoint        string `json:"endpoint"`
					Region          string `json:"region"`
				} `json:"secretS3"`
			} `json:"spec"`
		}
		if err := json.Unmarshal(raw, &info); err != nil {
			// BucketInfo is a best-effort fallback for fields the flat-key
			// renderer doesn't emit (e.g. region). If the flat keys are
			// already authoritative for access/secret, an unrelated stray
			// BucketInfo blob (e.g. left over from a copy-paste of an old
			// COSI Secret) should not terminally fail the projection.
			// Hard-fail only when BOTH the flat keys are missing AND the
			// JSON cannot be parsed — that's a Secret with no usable
			// credentials material.
			if out.accessKey == "" || out.secretKey == "" {
				return parsedCreds{}, &ProjectionError{
					Reason:  ReasonSourceMalformed,
					Message: fmt.Sprintf("parse BucketInfo: %v", err),
				}
			}
			// Soft-skip: flat keys win, BucketInfo not consulted further.
			return finaliseParsedCreds(out, src)
		}
		if out.accessKey == "" {
			out.accessKey = info.Spec.SecretS3.AccessKeyID
		}
		if out.secretKey == "" {
			out.secretKey = info.Spec.SecretS3.AccessSecretKey
		}
		if out.endpoint == "" {
			out.endpoint = info.Spec.SecretS3.Endpoint
		}
		if out.region == "" {
			out.region = info.Spec.SecretS3.Region
		}
		if out.bucket == "" {
			out.bucket = info.Spec.BucketName
		}
	}

	return finaliseParsedCreds(out, src)
}

// finaliseParsedCreds runs the post-parse validation + scheme stripping
// path used by both the happy path and the soft-skip-on-garbled-JSON
// path. Returns the typed ReasonSourceMalformed when access/secret keys
// did not land from either source.
func finaliseParsedCreds(out parsedCreds, src *corev1.Secret) (parsedCreds, error) {
	if out.accessKey == "" {
		return parsedCreds{}, &ProjectionError{
			Reason:  ReasonSourceMalformed,
			Message: fmt.Sprintf("source credentials Secret %s/%s missing accessKey", src.Namespace, src.Name),
		}
	}
	if out.secretKey == "" {
		return parsedCreds{}, &ProjectionError{
			Reason:  ReasonSourceMalformed,
			Message: fmt.Sprintf("source credentials Secret %s/%s missing secretKey", src.Namespace, src.Name),
		}
	}
	out.endpoint = stripScheme(out.endpoint)
	return out, nil
}

// buildVeleroCredentialsFile renders the AWS credentials file Velero
// expects when reading from a Secret-backed BackupStorageLocation. The
// format matches `aws configure`'s ~/.aws/credentials.
func buildVeleroCredentialsFile(accessKey, secretKey string) string {
	return fmt.Sprintf("[default]\naws_access_key_id=%s\naws_secret_access_key=%s\n", accessKey, secretKey)
}

// buildFDBBlobCredentials renders the JSON file the FoundationDB
// backup_agent reads when invoked with --blob_credentials=<path>. The
// account key is the bare host:port (no scheme); the FoundationDB
// strategy CR's accountName field carries the same value so they line up.
//
// Returns a typed error when endpoint is empty so the caller can surface
// a clear reason rather than write an account keyed by "".
func buildFDBBlobCredentials(endpoint, accessKey, secretKey string) ([]byte, error) {
	host := stripScheme(endpoint)
	if host == "" {
		return nil, fmt.Errorf("endpoint is empty; cannot build blob_credentials.json")
	}
	doc := map[string]any{
		"accounts": map[string]any{
			host: map[string]string{
				"api_key": accessKey,
				"secret":  secretKey,
			},
		},
	}
	return json.Marshal(doc)
}

// stripScheme returns the endpoint stripped of any leading scheme prefix
// (http:// or https://) and preserves the path portion if present. The
// resulting form is what `host[:port][/path]` callers (MariaDB strategy,
// FDB blob_credentials.json account host) expect.
//
// Implementation note: url.Parse on a bare `host:port` mis-parses port
// as the URL scheme (no error, Scheme="host", Host=""). We only treat
// url.Parse output as authoritative when an explicit scheme prefix is
// present; otherwise the input is assumed to already be in host[:port]
// form and returned as-is.
func stripScheme(endpoint string) string {
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		// Fallback: trim manually. Preserve path component intact.
		host := strings.TrimPrefix(endpoint, "http://")
		host = strings.TrimPrefix(host, "https://")
		return host
	}
	if u.Path != "" && u.Path != "/" {
		return u.Host + u.Path
	}
	return u.Host
}
