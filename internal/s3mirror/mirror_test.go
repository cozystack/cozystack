// SPDX-License-Identifier: Apache-2.0
package s3mirror

import (
	"bytes"
	"context"
	"io"
	"sort"
	"strings"
	"testing"
)

type fakeObject struct {
	data            []byte
	contentType     string
	contentEncoding string
	cacheControl    string
	userMetadata    map[string]string
}

// fakeStore is an in-memory objectStore: buckets[bucket][key] = object.
type fakeStore struct {
	buckets map[string]map[string]fakeObject
}

func newFakeStore() *fakeStore {
	return &fakeStore{buckets: map[string]map[string]fakeObject{}}
}

func (s *fakeStore) seed(bucket, key string, obj fakeObject) {
	if s.buckets[bucket] == nil {
		s.buckets[bucket] = map[string]fakeObject{}
	}
	s.buckets[bucket][key] = obj
}

func (s *fakeStore) list(_ context.Context, bucket, prefix string, fn func(key string, size int64) error) error {
	keys := make([]string, 0, len(s.buckets[bucket]))
	for k := range s.buckets[bucket] {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := fn(k, int64(len(s.buckets[bucket][k].data))); err != nil {
			return err
		}
	}
	return nil
}

func (s *fakeStore) get(_ context.Context, bucket, key string) (object, error) {
	o := s.buckets[bucket][key]
	return object{
		size:            int64(len(o.data)),
		contentType:     o.contentType,
		contentEncoding: o.contentEncoding,
		cacheControl:    o.cacheControl,
		userMetadata:    o.userMetadata,
		body:            io.NopCloser(bytes.NewReader(o.data)),
	}, nil
}

func (s *fakeStore) put(_ context.Context, bucket, key string, obj object) error {
	data, err := io.ReadAll(obj.body)
	if err != nil {
		return err
	}
	s.seed(bucket, key, fakeObject{
		data:            data,
		contentType:     obj.contentType,
		contentEncoding: obj.contentEncoding,
		cacheControl:    obj.cacheControl,
		userMetadata:    obj.userMetadata,
	})
	return nil
}

func (s *fakeStore) copyServerSide(_ context.Context, dstBucket, dstKey, srcBucket, srcKey string) error {
	s.seed(dstBucket, dstKey, s.buckets[srcBucket][srcKey])
	return nil
}

func (s *fakeStore) remove(_ context.Context, bucket, key string) error {
	delete(s.buckets[bucket], key)
	return nil
}

func (s *fakeStore) keys(bucket string) []string {
	out := make([]string, 0, len(s.buckets[bucket]))
	for k := range s.buckets[bucket] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestMirrorRestoreDeletesExtraneous(t *testing.T) {
	s := newFakeStore()
	s.seed("repo", "p/a", fakeObject{data: []byte("A")})
	s.seed("repo", "p/b", fakeObject{data: []byte("B")})
	s.seed("app", "a", fakeObject{data: []byte("old")})
	s.seed("app", "b", fakeObject{data: []byte("old")})
	s.seed("app", "c", fakeObject{data: []byte("extraneous")})

	from := copySide{store: s, bucket: "repo", prefix: "p/"}
	to := copySide{store: s, bucket: "app", prefix: ""}
	copied, err := mirror(context.Background(), from, to, false, true, false)
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	if copied != 2 {
		t.Fatalf("copied = %d, want 2", copied)
	}
	if got := strings.Join(s.keys("app"), ","); got != "a,b" {
		t.Fatalf("app keys = %q, want \"a,b\" (c must be purged)", got)
	}
	if string(s.buckets["app"]["a"].data) != "A" {
		t.Fatalf("app/a = %q, want restored bytes \"A\"", s.buckets["app"]["a"].data)
	}
}

func TestMirrorZeroObjectSourceDoesNotWipe(t *testing.T) {
	s := newFakeStore()
	// Nothing under repo/p/ — the snapshot's objects are gone.
	s.seed("app", "a", fakeObject{data: []byte("live")})
	s.seed("app", "b", fakeObject{data: []byte("live")})

	from := copySide{store: s, bucket: "repo", prefix: "p/"}
	to := copySide{store: s, bucket: "app", prefix: ""}

	copied, err := mirror(context.Background(), from, to, false, true, false)
	if err == nil {
		t.Fatal("mirror: want error on zero-object source with delete-extraneous, got nil")
	}
	if copied != 0 {
		t.Fatalf("copied = %d, want 0", copied)
	}
	if got := strings.Join(s.keys("app"), ","); got != "a,b" {
		t.Fatalf("app keys = %q, want \"a,b\" (live bucket must be untouched)", got)
	}

	// The override still lets an intentional empty-source restore through.
	if _, err := mirror(context.Background(), from, to, false, true, true); err != nil {
		t.Fatalf("mirror(allow-empty-source): %v", err)
	}
	if got := s.keys("app"); len(got) != 0 {
		t.Fatalf("app keys = %v, want empty after allow-empty-source purge", got)
	}
}

func TestMirrorPreservesObjectMetadata(t *testing.T) {
	s := newFakeStore()
	s.seed("repo", "p/x", fakeObject{
		data:            []byte("PNGDATA"),
		contentType:     "image/png",
		contentEncoding: "gzip",
		cacheControl:    "max-age=3600",
		userMetadata:    map[string]string{"Origin": "unit"},
	})

	from := copySide{store: s, bucket: "repo", prefix: "p/"}
	to := copySide{store: s, bucket: "app", prefix: ""}
	if _, err := mirror(context.Background(), from, to, false, false, false); err != nil {
		t.Fatalf("mirror: %v", err)
	}

	got := s.buckets["app"]["x"]
	if got.contentType != "image/png" {
		t.Errorf("contentType = %q, want image/png", got.contentType)
	}
	if got.contentEncoding != "gzip" {
		t.Errorf("contentEncoding = %q, want gzip", got.contentEncoding)
	}
	if got.cacheControl != "max-age=3600" {
		t.Errorf("cacheControl = %q, want max-age=3600", got.cacheControl)
	}
	if got.userMetadata["Origin"] != "unit" {
		t.Errorf("userMetadata[Origin] = %q, want unit", got.userMetadata["Origin"])
	}
	if string(got.data) != "PNGDATA" {
		t.Errorf("data = %q, want PNGDATA", got.data)
	}
}
