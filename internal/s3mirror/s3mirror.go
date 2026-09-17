// SPDX-License-Identifier: Apache-2.0

// Package s3mirror implements the `s3-mirror` subcommand of the
// backupstrategy-controller binary: a one-shot S3-to-S3 object copy that the
// Bucket backup driver runs as a Kubernetes Job. Keeping it in the controller
// binary (rather than shipping a separate image) keeps the whole backup stack
// on Apache-2.0 dependencies - the copy is built on the minio-go SDK, not the
// AGPL `mc` client - and lets the Job reuse the controller image.
//
// Direction is set by --mode: `backup` reads the application bucket and writes
// under the repo prefix; `restore` reads the repo prefix and writes the
// application bucket, purging objects the snapshot does not contain when
// --delete-extraneous is set (an in-place restore is then a true mirror).
package s3mirror

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// endpointCreds is the coordinate + credential pair for one side of the copy.
type endpointCreds struct {
	endpoint  string // may carry a scheme; normalised by newClient
	accessKey string
	secretKey string
	bucket    string
	region    string
}

// bucketInfo is the subset of the COSI BucketInfo document the mirror reads
// for the application-side bucket.
type bucketInfo struct {
	AccessKey string
	SecretKey string
	Endpoint  string
	Bucket    string
}

// parseBucketInfo extracts the S3 coordinates from a COSI BucketInfo JSON
// document (the single "BucketInfo" key of a BucketAccess credentials Secret).
// Pure so it can be unit-tested without a live cluster.
func parseBucketInfo(raw []byte) (bucketInfo, error) {
	var doc struct {
		Spec struct {
			BucketName string `json:"bucketName"`
			SecretS3   struct {
				AccessKeyID     string `json:"accessKeyID"`
				AccessSecretKey string `json:"accessSecretKey"`
				Endpoint        string `json:"endpoint"`
			} `json:"secretS3"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return bucketInfo{}, fmt.Errorf("parse BucketInfo: %w", err)
	}
	bi := bucketInfo{
		AccessKey: doc.Spec.SecretS3.AccessKeyID,
		SecretKey: doc.Spec.SecretS3.AccessSecretKey,
		Endpoint:  doc.Spec.SecretS3.Endpoint,
		Bucket:    doc.Spec.BucketName,
	}
	if bi.AccessKey == "" || bi.SecretKey == "" || bi.Endpoint == "" || bi.Bucket == "" {
		return bucketInfo{}, fmt.Errorf("BucketInfo is missing one of accessKeyID/accessSecretKey/endpoint/bucketName")
	}
	return bi, nil
}

// splitEndpoint returns the host (scheme stripped) and whether TLS is used.
// A bare host defaults to secure, since cozystack's SeaweedFS S3 serves TLS.
// Pure and unit-testable.
func splitEndpoint(endpoint string) (host string, secure bool) {
	switch {
	case strings.HasPrefix(endpoint, "https://"):
		return strings.TrimPrefix(endpoint, "https://"), true
	case strings.HasPrefix(endpoint, "http://"):
		return strings.TrimPrefix(endpoint, "http://"), false
	default:
		return endpoint, true
	}
}

// Run executes the s3-mirror subcommand. Returns a process exit code so the
// caller can os.Exit on it.
func Run(args []string) int {
	fs := flag.NewFlagSet("s3-mirror", flag.ContinueOnError)
	mode := fs.String("mode", "", "backup, restore or delete")
	repoEndpoint := fs.String("repo-endpoint", "", "repo (cozy-backups) S3 endpoint")
	repoBucket := fs.String("repo-bucket", "", "repo S3 bucket name")
	repoPrefix := fs.String("repo-prefix", "", "repo object-key prefix for this backup")
	repoRegion := fs.String("repo-region", "", "repo S3 region")
	appEndpoint := fs.String("app-endpoint", "", "override the application-side endpoint from APP_BUCKETINFO")
	insecure := fs.Bool("insecure", false, "skip TLS certificate verification")
	caFile := fs.String("ca-file", "", "PEM CA bundle to trust")
	serverSide := fs.Bool("server-side", false, "attempt server-side CopyObject before streaming")
	deleteExtraneous := fs.Bool("delete-extraneous", false, "delete destination objects absent from the source (restore mirror)")
	allowEmptySource := fs.Bool("allow-empty-source", false, "permit a restore whose source lists zero objects (which would wipe or empty the destination)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *mode != "backup" && *mode != "restore" && *mode != "delete" {
		fmt.Fprintln(os.Stderr, "s3-mirror: --mode must be backup, restore or delete")
		return 2
	}
	if *repoEndpoint == "" || *repoBucket == "" {
		fmt.Fprintln(os.Stderr, "s3-mirror: --repo-endpoint and --repo-bucket are required")
		return 2
	}
	// A restore reads, and a delete removes, the repo side under this prefix; an
	// empty one would sweep the whole repo bucket (every snapshot and tenant).
	// Backup mode legitimately reads the application bucket at an empty prefix,
	// so gate this on restore/delete only.
	if (*mode == "restore" || *mode == "delete") && *repoPrefix == "" {
		fmt.Fprintln(os.Stderr, "s3-mirror: --repo-prefix is required in "+*mode+" mode")
		return 2
	}
	// --delete-extraneous removes objects the source does not contain; without a
	// prefix to scope it, an accidental backup-mode run would sweep the whole
	// repo bucket. No caller emits this pair, but a delete stays gated on scope
	// rather than trusting the caller.
	if *deleteExtraneous && *repoPrefix == "" {
		fmt.Fprintln(os.Stderr, "s3-mirror: --delete-extraneous requires --repo-prefix")
		return 2
	}

	transport, err := buildTransport(*insecure, *caFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "s3-mirror: %v\n", err)
		return 1
	}
	if *mode == "delete" {
		repo := endpointCreds{
			endpoint:  *repoEndpoint,
			accessKey: os.Getenv("REPO_ACCESS_KEY"),
			secretKey: os.Getenv("REPO_SECRET_KEY"),
			bucket:    *repoBucket,
			region:    *repoRegion,
		}
		if repo.accessKey == "" || repo.secretKey == "" {
			fmt.Fprintln(os.Stderr, "s3-mirror: REPO_ACCESS_KEY/REPO_SECRET_KEY env is empty")
			return 1
		}
		repoClient, cerr := newClient(repo, transport)
		if cerr != nil {
			fmt.Fprintf(os.Stderr, "s3-mirror: repo client: %v\n", cerr)
			return 1
		}
		removed, derr := purge(context.Background(), copySide{store: &minioStore{client: repoClient}, bucket: repo.bucket, prefix: *repoPrefix})
		if derr != nil {
			fmt.Fprintf(os.Stderr, "s3-mirror: %v\n", derr)
			return 1
		}
		fmt.Printf("s3-mirror: delete complete, %d object(s) removed under %s/%s\n", removed, repo.bucket, *repoPrefix)
		return 0
	}

	// backup / restore need the application side too.
	appRaw := os.Getenv("APP_BUCKETINFO")
	if appRaw == "" {
		fmt.Fprintln(os.Stderr, "s3-mirror: APP_BUCKETINFO env is empty")
		return 1
	}
	bi, err := parseBucketInfo([]byte(appRaw))
	if err != nil {
		fmt.Fprintf(os.Stderr, "s3-mirror: %v\n", err)
		return 1
	}

	appEndpointResolved := bi.Endpoint
	if *appEndpoint != "" {
		appEndpointResolved = *appEndpoint
	}
	app := endpointCreds{endpoint: appEndpointResolved, accessKey: bi.AccessKey, secretKey: bi.SecretKey, bucket: bi.Bucket}
	repo := endpointCreds{
		endpoint:  *repoEndpoint,
		accessKey: os.Getenv("REPO_ACCESS_KEY"),
		secretKey: os.Getenv("REPO_SECRET_KEY"),
		bucket:    *repoBucket,
		region:    *repoRegion,
	}
	if repo.accessKey == "" || repo.secretKey == "" {
		fmt.Fprintln(os.Stderr, "s3-mirror: REPO_ACCESS_KEY/REPO_SECRET_KEY env is empty")
		return 1
	}

	appClient, err := newClient(app, transport)
	if err != nil {
		fmt.Fprintf(os.Stderr, "s3-mirror: app client: %v\n", err)
		return 1
	}
	repoClient, err := newClient(repo, transport)
	if err != nil {
		fmt.Fprintf(os.Stderr, "s3-mirror: repo client: %v\n", err)
		return 1
	}

	ctx := context.Background()
	appStore := &minioStore{client: appClient}
	repoStore := &minioStore{client: repoClient}
	var from, to copySide
	if *mode == "backup" {
		from = copySide{store: appStore, bucket: app.bucket, prefix: ""}
		to = copySide{store: repoStore, bucket: repo.bucket, prefix: *repoPrefix}
	} else {
		from = copySide{store: repoStore, bucket: repo.bucket, prefix: *repoPrefix}
		to = copySide{store: appStore, bucket: app.bucket, prefix: ""}
	}

	// A freshly granted COSI BucketAccess reports accessGranted before the S3
	// server has necessarily reloaded the new IAM identity, so the first request
	// can 403. Retry a light read against the application bucket until it works
	// (or times out) so the first BackupJob does not fail on propagation lag.
	if err := waitForBucketReady(ctx, appStore, app.bucket, bucketPreflightTimeout); err != nil {
		fmt.Fprintf(os.Stderr, "s3-mirror: application bucket preflight: %v\n", err)
		return 1
	}

	sameEndpoint := hostOf(app.endpoint) == hostOf(repo.endpoint)
	copied, err := mirror(ctx, from, to, *serverSide && sameEndpoint, *deleteExtraneous, *mode == "restore", *allowEmptySource)
	if err != nil {
		fmt.Fprintf(os.Stderr, "s3-mirror: %v\n", err)
		return 1
	}
	// A backup that copied nothing is legitimate (an empty application bucket)
	// but indistinguishable from a misconfiguration until someone restores it,
	// so surface it rather than reporting a silent success.
	if *mode == "backup" && copied == 0 {
		fmt.Fprintf(os.Stderr, "s3-mirror: warning: backup copied 0 objects from %s (empty bucket, or wrong coordinates)\n", from.bucket)
	}
	fmt.Printf("s3-mirror: %s complete, %d object(s) copied %s -> %s\n", *mode, copied, from.bucket, to.bucket)
	return 0
}

// bucketPreflightTimeout budgets how long to wait for a freshly granted
// BucketAccess to become usable, matching the e2e preflight's 90s.
const bucketPreflightTimeout = 90 * time.Second

// waitForBucketReady retries a light listing against bucket until it succeeds or
// timeout elapses, so a not-yet-propagated S3 credential does not fail the copy.
func waitForBucketReady(ctx context.Context, store objectStore, bucket string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	delay := time.Second
	for {
		// A prefix unlikely to match real keys keeps the probe cheap: a working
		// credential returns an empty listing, a not-yet-loaded one returns the
		// server's auth error. The prefix must stay plain ASCII - SeaweedFS
		// answers a list whose prefix carries a NUL/control byte with a 500
		// InternalError, which would make this readiness probe never succeed.
		err := store.list(ctx, bucket, ".cozy-s3-mirror-preflight", func(string, int64, string) error { return nil })
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("bucket %s not reachable within %s: %w", bucket, timeout, err)
		}
		time.Sleep(delay)
		if delay < 10*time.Second {
			delay *= 2
		}
	}
}

func hostOf(endpoint string) string {
	h, _ := splitEndpoint(endpoint)
	return h
}

func buildTransport(insecure bool, caFile string) (http.RoundTripper, error) {
	if !insecure && caFile == "" {
		return nil, nil
	}
	tlsConfig := &tls.Config{InsecureSkipVerify: insecure} //nolint:gosec // insecure is an explicit opt-in for self-signed in-cluster endpoints
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if os.IsNotExist(err) {
			// The CA Secret is mounted optionally, so an admin who set no CA (or
			// mis-mounted one) leaves the file absent: fall through to the system
			// trust store rather than failing the copy. A genuinely private-CA
			// endpoint then fails the handshake fast, which is the same signal.
			return &http.Transport{TLSClientConfig: tlsConfig}, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read CA file %s: %w", caFile, err)
		}
		// Append to the system roots rather than replacing them: this one
		// transport serves both the application and the repo endpoint, so a
		// private CA supplied for one side must not strip the public trust the
		// other side may rely on.
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates parsed from CA file %s", caFile)
		}
		tlsConfig.RootCAs = pool
	}
	return &http.Transport{TLSClientConfig: tlsConfig}, nil
}

func newClient(c endpointCreds, transport http.RoundTripper) (*minio.Client, error) {
	host, secure := splitEndpoint(c.endpoint)
	opts := &minio.Options{
		Creds:  credentials.NewStaticV4(c.accessKey, c.secretKey, ""),
		Secure: secure,
		Region: c.region,
	}
	if transport != nil {
		opts.Transport = transport
	}
	return minio.New(host, opts)
}

// object carries one S3 object's payload and the metadata the mirror preserves
// across a copy.
type object struct {
	size            int64
	contentType     string
	contentEncoding string
	cacheControl    string
	userMetadata    map[string]string
	body            io.ReadCloser
}

// objectStore is the minimal S3 surface the mirror needs. It is an interface so
// the copy/purge decision logic - the destructive --delete-extraneous path in
// particular - is unit-testable against an in-memory fake.
type objectStore interface {
	// list streams the key+size of every object under prefix to fn; an error
	// from fn or the listing aborts and propagates.
	list(ctx context.Context, bucket, prefix string, fn func(key string, size int64, etag string) error) error
	// get opens one object; the caller closes the returned body.
	get(ctx context.Context, bucket, key string) (object, error)
	// statETag reports an object's ETag (content identity) and whether it exists.
	statETag(ctx context.Context, bucket, key string) (etag string, exists bool, err error)
	put(ctx context.Context, bucket, key string, obj object) error
	// copyServerSide attempts an in-server copy; a returned error signals the
	// caller to fall back to a streamed copy.
	copyServerSide(ctx context.Context, dstBucket, dstKey, srcBucket, srcKey string) error
	remove(ctx context.Context, bucket, key string) error
}

// copySide is one store+bucket+prefix in a mirror.
type copySide struct {
	store  objectStore
	bucket string
	prefix string
}

// mirror copies every object under from into to (rekeying from.prefix to
// to.prefix), optionally deleting objects under to.prefix that the source no
// longer contains. Returns the number of objects copied. restore marks the
// direction as a restore, where a zero-object source is refused (see below).
func mirror(ctx context.Context, from, to copySide, serverSide, deleteExtraneous, restore, allowEmptySource bool) (int, error) {
	seen := map[string]struct{}{}
	copied := 0
	skipped := 0

	err := from.store.list(ctx, from.bucket, from.prefix, func(key string, _ int64, etag string) error {
		rel := strings.TrimPrefix(key, from.prefix)
		destKey := to.prefix + rel
		seen[destKey] = struct{}{}

		// Resume: skip an object already at the destination with the SAME content
		// (matching ETag), so a retried run does not re-transfer what an earlier
		// attempt copied. Comparing content, not size, is what keeps an in-place
		// restore faithful: an object modified in place at the same byte size has
		// a different ETag, so it is re-copied to the snapshot content rather than
		// left as the live bytes. An empty source ETag (some backends omit it)
		// falls through to a copy, which is safe.
		if etag != "" {
			if detag, ok, serr := to.store.statETag(ctx, to.bucket, destKey); serr != nil {
				return serr
			} else if ok && detag == etag {
				skipped++
				return nil
			}
		}

		if serverSide {
			if err := to.store.copyServerSide(ctx, to.bucket, destKey, from.bucket, key); err == nil {
				copied++
				return nil
			}
			// Fall back to a streamed copy: a single credential may not be
			// authorized on both buckets, so a server-side copy can fail
			// where a two-client streamed copy succeeds.
		}

		if err := streamCopy(ctx, from, to, key, destKey); err != nil {
			return err
		}
		copied++
		return nil
	})
	if err != nil {
		return copied, err
	}

	// A restore that copies nothing means the snapshot's objects are gone
	// (retention-pruned, expired) or the source was empty at backup time. An
	// in-place restore would then delete every live object, and a to-copy
	// restore would silently "succeed" having restored nothing - so refuse both.
	// Backup mode legitimately copies zero from an empty application bucket, so
	// this is gated on restore. --allow-empty-source overrides it.
	if restore && copied+skipped == 0 && !allowEmptySource {
		detail := "would restore nothing"
		if deleteExtraneous {
			detail = "would delete every object in the destination"
		}
		return copied, fmt.Errorf("refusing to restore from empty snapshot %s/%s: it lists zero objects and %s", from.bucket, from.prefix, detail)
	}

	if deleteExtraneous {
		if err := deleteUnseen(ctx, to, seen); err != nil {
			return copied, err
		}
	}
	return copied, nil
}

func streamCopy(ctx context.Context, from, to copySide, srcKey, destKey string) error {
	obj, err := from.store.get(ctx, from.bucket, srcKey)
	if err != nil {
		return fmt.Errorf("get %s/%s: %w", from.bucket, srcKey, err)
	}
	defer obj.body.Close()
	if err := to.store.put(ctx, to.bucket, destKey, obj); err != nil {
		return fmt.Errorf("put %s/%s: %w", to.bucket, destKey, err)
	}
	return nil
}

// purge removes every object under side.prefix and returns how many it deleted.
// It is the delete-mode counterpart of deleteUnseen with an empty seen set,
// used to reclaim a backup's repo objects when its Backup is deleted.
func purge(ctx context.Context, side copySide) (int, error) {
	removed := 0
	err := side.store.list(ctx, side.bucket, side.prefix, func(key string, _ int64, _ string) error {
		if err := side.store.remove(ctx, side.bucket, key); err != nil {
			return fmt.Errorf("remove %s/%s: %w", side.bucket, key, err)
		}
		removed++
		return nil
	})
	return removed, err
}

func deleteUnseen(ctx context.Context, to copySide, seen map[string]struct{}) error {
	return to.store.list(ctx, to.bucket, to.prefix, func(key string, _ int64, _ string) error {
		if _, ok := seen[key]; ok {
			return nil
		}
		if err := to.store.remove(ctx, to.bucket, key); err != nil {
			return fmt.Errorf("remove %s/%s: %w", to.bucket, key, err)
		}
		return nil
	})
}

// minioStore adapts a *minio.Client to objectStore; each method maps to the SDK
// call the mirror previously made inline.
type minioStore struct {
	client *minio.Client
}

func (s *minioStore) list(ctx context.Context, bucket, prefix string, fn func(key string, size int64, etag string) error) error {
	for info := range s.client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if info.Err != nil {
			return fmt.Errorf("list %s/%s: %w", bucket, prefix, info.Err)
		}
		if err := fn(info.Key, info.Size, normalizeETag(info.ETag)); err != nil {
			return err
		}
	}
	return nil
}

func (s *minioStore) get(ctx context.Context, bucket, key string) (object, error) {
	o, err := s.client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return object{}, err
	}
	// Stat resolves the object's headers (Content-Type, Content-Encoding, user
	// metadata) that a listing does not carry; a subsequent Read still streams
	// the body from offset 0.
	info, err := o.Stat()
	if err != nil {
		o.Close()
		return object{}, err
	}
	return object{
		size:            info.Size,
		contentType:     info.ContentType,
		contentEncoding: info.Metadata.Get("Content-Encoding"),
		cacheControl:    info.Metadata.Get("Cache-Control"),
		userMetadata:    userMetadataOf(info.Metadata),
		body:            o,
	}, nil
}

func (s *minioStore) statETag(ctx context.Context, bucket, key string) (string, bool, error) {
	info, err := s.client.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if err != nil {
		resp := minio.ToErrorResponse(err)
		if resp.Code == "NoSuchKey" || resp.StatusCode == http.StatusNotFound {
			return "", false, nil
		}
		return "", false, err
	}
	return normalizeETag(info.ETag), true, nil
}

// normalizeETag strips the optional surrounding quotes S3 ETags carry so a
// listing ETag and a stat ETag for the same object compare equal.
func normalizeETag(etag string) string {
	return strings.Trim(etag, `"`)
}

func (s *minioStore) put(ctx context.Context, bucket, key string, obj object) error {
	_, err := s.client.PutObject(ctx, bucket, key, obj.body, obj.size, minio.PutObjectOptions{
		ContentType:     obj.contentType,
		ContentEncoding: obj.contentEncoding,
		CacheControl:    obj.cacheControl,
		UserMetadata:    obj.userMetadata,
	})
	return err
}

func (s *minioStore) copyServerSide(ctx context.Context, dstBucket, dstKey, srcBucket, srcKey string) error {
	_, err := s.client.CopyObject(ctx,
		minio.CopyDestOptions{Bucket: dstBucket, Object: dstKey},
		minio.CopySrcOptions{Bucket: srcBucket, Object: srcKey})
	return err
}

func (s *minioStore) remove(ctx context.Context, bucket, key string) error {
	return s.client.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{})
}

// userMetadataOf lifts the x-amz-meta-* headers (prefix stripped) that minio-go
// exposes on ObjectInfo.Metadata into the map PutObject re-emits.
func userMetadataOf(h http.Header) map[string]string {
	var out map[string]string
	const prefix = "X-Amz-Meta-"
	for k, v := range h {
		if len(v) == 0 {
			continue
		}
		if ck := http.CanonicalHeaderKey(k); strings.HasPrefix(ck, prefix) {
			if out == nil {
				out = map[string]string{}
			}
			out[strings.TrimPrefix(ck, prefix)] = v[0]
		}
	}
	return out
}
