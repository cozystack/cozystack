// SPDX-License-Identifier: Apache-2.0
package s3mirror

import "testing"

func TestRunRejectsEmptyRestorePrefix(t *testing.T) {
	// An empty --repo-prefix in restore mode would list the whole repo bucket;
	// Run must reject it before contacting S3. Validation runs before any env
	// lookup, so no APP_BUCKETINFO/REPO_* is needed to reach it.
	code := Run([]string{"--mode=restore", "--repo-endpoint=s3.example.com", "--repo-bucket=cozy-backups"})
	if code != 2 {
		t.Fatalf("Run(restore, empty prefix) = %d, want 2", code)
	}
}

func TestRunRejectsDeleteExtraneousWithoutPrefix(t *testing.T) {
	// backup mode allows an empty prefix, but --delete-extraneous without one
	// would sweep the whole repo bucket, so Run must reject the pair regardless
	// of mode.
	code := Run([]string{"--mode=backup", "--repo-endpoint=s3.example.com", "--repo-bucket=cozy-backups", "--delete-extraneous"})
	if code != 2 {
		t.Fatalf("Run(backup, --delete-extraneous, no prefix) = %d, want 2", code)
	}
}

func TestParseBucketInfo(t *testing.T) {
	valid := `{"spec":{"bucketName":"bucket-abc","secretS3":{"accessKeyID":"AK","accessSecretKey":"SK","endpoint":"https://s3.example.com:8333"}}}`
	bi, err := parseBucketInfo([]byte(valid))
	if err != nil {
		t.Fatalf("parseBucketInfo(valid): %v", err)
	}
	if bi.AccessKey != "AK" || bi.SecretKey != "SK" || bi.Bucket != "bucket-abc" || bi.Endpoint != "https://s3.example.com:8333" {
		t.Fatalf("parsed = %+v", bi)
	}

	if _, err := parseBucketInfo([]byte(`{`)); err == nil {
		t.Fatal("parseBucketInfo(malformed): want error")
	}
	if _, err := parseBucketInfo([]byte(`{"spec":{"bucketName":"b"}}`)); err == nil {
		t.Fatal("parseBucketInfo(missing creds): want error")
	}
}

func TestSplitEndpoint(t *testing.T) {
	cases := []struct {
		in         string
		wantHost   string
		wantSecure bool
	}{
		{"https://s3.example.com:8333", "s3.example.com:8333", true},
		{"http://s3.example.com:8333", "s3.example.com:8333", false},
		{"s3.example.com:8333", "s3.example.com:8333", true},
	}
	for _, tc := range cases {
		host, secure := splitEndpoint(tc.in)
		if host != tc.wantHost || secure != tc.wantSecure {
			t.Fatalf("splitEndpoint(%q) = (%q, %v), want (%q, %v)", tc.in, host, secure, tc.wantHost, tc.wantSecure)
		}
	}
}
