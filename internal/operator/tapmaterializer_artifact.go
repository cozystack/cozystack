/*
Copyright 2025 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package operator

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/marketplace/tapconst"
)

// maxArtifactBytes bounds the decompressed size of a tapped artifact so a
// malicious or corrupt tarball cannot exhaust memory or disk.
const maxArtifactBytes = 256 << 20 // 256 MiB

// verifyAndExtract checks that data hashes to expectedDigest (a Flux
// "sha256:<hex>" digest), then extracts the gzipped tarball into destDir. It
// rejects path traversal and caps the total extracted size. A digest mismatch
// is a hard error so a tampered artifact is never materialized.
func verifyAndExtract(data []byte, expectedDigest, destDir string) error {
	// Fail closed: this handles untrusted remote content, so an absent digest is
	// an error, not a reason to skip verification.
	if expectedDigest == "" {
		return fmt.Errorf("refusing to extract a tap artifact with no digest to verify against")
	}
	sum := sha256.Sum256(data)
	got := "sha256:" + hex.EncodeToString(sum[:])
	if got != expectedDigest {
		return fmt.Errorf("artifact digest mismatch: got %s, want %s", got, expectedDigest)
	}

	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	var written int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		target, err := safeJoin(destDir, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			written += hdr.Size
			if written > maxArtifactBytes {
				return fmt.Errorf("artifact exceeds %d bytes", maxArtifactBytes)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.CopyN(f, tr, hdr.Size); err != nil {
				_ = f.Close()
				return err
			}
			_ = f.Close()
		default:
			// Skip symlinks, devices, etc.: an app artifact needs only files.
		}
	}
	return nil
}

// safeJoin joins name under base, rejecting entries that escape base. It
// rejects any ".." component outright (the traversal marker) and, as a second
// guard, confirms the resolved path stays within base.
func safeJoin(base, name string) (string, error) {
	if name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") || strings.HasSuffix(name, "/..") {
		return "", fmt.Errorf("tar entry %q contains a path-traversal segment", name)
	}
	target := filepath.Join(base, name)
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("tar entry %q escapes the extraction directory", name)
	}
	return target, nil
}

// parsePackageSourcesFromTree walks an extracted artifact and strict-decodes
// every PackageSource manifest it finds. It splits multi-document YAML with the
// apimachinery reader, which honours block scalars (a naive `^---$` split would
// fracture a document whose values contain such a line).
func parsePackageSourcesFromTree(root string) ([]cozyv1alpha1.PackageSource, error) {
	var out []cozyv1alpha1.PackageSource
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || (!strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml")) {
			return nil
		}
		data, e := os.ReadFile(path)
		if e != nil {
			return nil
		}
		reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
		for {
			doc, rerr := reader.Read()
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				return fmt.Errorf("%s: read YAML: %w", filepath.Base(path), rerr)
			}
			if len(bytes.TrimSpace(doc)) == 0 {
				continue
			}
			var tm metav1.TypeMeta
			if err := yaml.Unmarshal(doc, &tm); err != nil || tm.Kind != "PackageSource" {
				continue
			}
			var ps cozyv1alpha1.PackageSource
			if err := yaml.UnmarshalStrict(doc, &ps); err != nil {
				return fmt.Errorf("%s: decode PackageSource: %w", filepath.Base(path), err)
			}
			out = append(out, ps)
		}
		return nil
	})
	return out, err
}

// materializedName derives the cluster-scoped name for a PackageSource pulled
// from a tapped artifact. base is the community.<org>.<repo> name recorded on
// the tap; single indicates the artifact carries exactly one PackageSource.
func materializedName(base, originalName string, single bool) string {
	if single {
		return base
	}
	return base + "." + strings.TrimPrefix(originalName, tapconst.Prefix)
}

// communityBaseFromURL derives a community.<org>.<repo> base name from an OCI
// URL, a fallback for when the tap-name annotation is absent.
func communityBaseFromURL(url string) string {
	body := strings.TrimPrefix(url, "oci://")
	if i := strings.LastIndex(body, "@"); i >= 0 {
		body = body[:i]
	}
	if i := strings.LastIndex(body, ":"); i >= 0 && !strings.Contains(body[i:], "/") {
		body = body[:i]
	}
	segs := strings.Split(strings.Trim(body, "/"), "/")
	base := tapconst.Prefix
	if len(segs) >= 3 {
		base += segs[len(segs)-2] + "."
	}
	base += segs[len(segs)-1]
	return base
}

// rewriteForMaterialize renames a PackageSource and repoints its sourceRef at
// the tap's Flux source, clearing server-set metadata for a clean apply.
func rewriteForMaterialize(ps *cozyv1alpha1.PackageSource, newName, sourceName, sourceNamespace, sourcePath string) {
	ps.SetName(newName)
	ps.SetResourceVersion("")
	ps.SetUID("")
	if sourcePath == "" {
		sourcePath = "/"
	}
	ps.Spec.SourceRef = &cozyv1alpha1.PackageSourceRef{
		Kind:      "OCIRepository",
		Name:      sourceName,
		Namespace: sourceNamespace,
		Path:      sourcePath,
	}
}
