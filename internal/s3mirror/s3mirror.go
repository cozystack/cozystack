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
	"net/http"
	"os"
	"strings"

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
	mode := fs.String("mode", "", "backup or restore")
	repoEndpoint := fs.String("repo-endpoint", "", "repo (cozy-backups) S3 endpoint")
	repoBucket := fs.String("repo-bucket", "", "repo S3 bucket name")
	repoPrefix := fs.String("repo-prefix", "", "repo object-key prefix for this backup")
	repoRegion := fs.String("repo-region", "", "repo S3 region")
	appEndpoint := fs.String("app-endpoint", "", "override the application-side endpoint from APP_BUCKETINFO")
	insecure := fs.Bool("insecure", false, "skip TLS certificate verification")
	caFile := fs.String("ca-file", "", "PEM CA bundle to trust")
	serverSide := fs.Bool("server-side", false, "attempt server-side CopyObject before streaming")
	deleteExtraneous := fs.Bool("delete-extraneous", false, "delete destination objects absent from the source (restore mirror)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *mode != "backup" && *mode != "restore" {
		fmt.Fprintln(os.Stderr, "s3-mirror: --mode must be backup or restore")
		return 2
	}
	if *repoEndpoint == "" || *repoBucket == "" {
		fmt.Fprintln(os.Stderr, "s3-mirror: --repo-endpoint and --repo-bucket are required")
		return 2
	}

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

	transport, err := buildTransport(*insecure, *caFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "s3-mirror: %v\n", err)
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
	var from, to copySide
	if *mode == "backup" {
		from = copySide{client: appClient, bucket: app.bucket, prefix: ""}
		to = copySide{client: repoClient, bucket: repo.bucket, prefix: *repoPrefix}
	} else {
		from = copySide{client: repoClient, bucket: repo.bucket, prefix: *repoPrefix}
		to = copySide{client: appClient, bucket: app.bucket, prefix: ""}
	}

	sameEndpoint := hostOf(app.endpoint) == hostOf(repo.endpoint)
	copied, err := mirror(ctx, from, to, *serverSide && sameEndpoint, *deleteExtraneous)
	if err != nil {
		fmt.Fprintf(os.Stderr, "s3-mirror: %v\n", err)
		return 1
	}
	fmt.Printf("s3-mirror: %s complete, %d object(s) copied %s -> %s\n", *mode, copied, from.bucket, to.bucket)
	return 0
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
		if err != nil {
			return nil, fmt.Errorf("read CA file %s: %w", caFile, err)
		}
		pool := x509.NewCertPool()
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

// copySide is one endpoint+bucket+prefix in a mirror.
type copySide struct {
	client *minio.Client
	bucket string
	prefix string
}

// mirror copies every object under from into to (rekeying from.prefix to
// to.prefix), optionally deleting objects under to.prefix that the source no
// longer contains. Returns the number of objects copied.
func mirror(ctx context.Context, from, to copySide, serverSide, deleteExtraneous bool) (int, error) {
	seen := map[string]struct{}{}
	copied := 0

	for obj := range from.client.ListObjects(ctx, from.bucket, minio.ListObjectsOptions{Prefix: from.prefix, Recursive: true}) {
		if obj.Err != nil {
			return copied, fmt.Errorf("list %s/%s: %w", from.bucket, from.prefix, obj.Err)
		}
		rel := strings.TrimPrefix(obj.Key, from.prefix)
		destKey := to.prefix + rel
		seen[destKey] = struct{}{}

		if serverSide {
			_, err := to.client.CopyObject(ctx,
				minio.CopyDestOptions{Bucket: to.bucket, Object: destKey},
				minio.CopySrcOptions{Bucket: from.bucket, Object: obj.Key})
			if err == nil {
				copied++
				continue
			}
			// Fall back to a streamed copy: a single credential may not be
			// authorized on both buckets, so a server-side copy can fail
			// where a two-client streamed copy succeeds.
		}

		if err := streamCopy(ctx, from, to, obj.Key, destKey, obj.Size); err != nil {
			return copied, err
		}
		copied++
	}

	if deleteExtraneous {
		if err := deleteUnseen(ctx, to, seen); err != nil {
			return copied, err
		}
	}
	return copied, nil
}

func streamCopy(ctx context.Context, from, to copySide, srcKey, destKey string, size int64) error {
	obj, err := from.client.GetObject(ctx, from.bucket, srcKey, minio.GetObjectOptions{})
	if err != nil {
		return fmt.Errorf("get %s/%s: %w", from.bucket, srcKey, err)
	}
	defer obj.Close()
	if _, err := to.client.PutObject(ctx, to.bucket, destKey, obj, size, minio.PutObjectOptions{}); err != nil {
		return fmt.Errorf("put %s/%s: %w", to.bucket, destKey, err)
	}
	return nil
}

func deleteUnseen(ctx context.Context, to copySide, seen map[string]struct{}) error {
	for obj := range to.client.ListObjects(ctx, to.bucket, minio.ListObjectsOptions{Prefix: to.prefix, Recursive: true}) {
		if obj.Err != nil {
			return fmt.Errorf("list %s/%s: %w", to.bucket, to.prefix, obj.Err)
		}
		if _, ok := seen[obj.Key]; ok {
			continue
		}
		if err := to.client.RemoveObject(ctx, to.bucket, obj.Key, minio.RemoveObjectOptions{}); err != nil {
			return fmt.Errorf("remove %s/%s: %w", to.bucket, obj.Key, err)
		}
	}
	return nil
}
