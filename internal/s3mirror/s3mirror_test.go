// SPDX-License-Identifier: Apache-2.0
package s3mirror

import "testing"

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
