// Package imageoverrides turns the platform's image override table into the
// Helm post-renderer that applies it.
//
// Registry mirrors on the nodes can only serve an image under the name it was
// pinned with. An override can also rename it, retag it and repin its digest,
// which is what an image set rebuilt under other names needs. The table lives
// in the platform values (registries.imageOverrides) and reaches every release
// as _cluster.image-overrides in the cozystack-values Secret; the HelmReleases
// built in Go read it from there, and the ones charts render get it through
// the cozy-lib.postRenderers helper.
package imageoverrides

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/apis/kustomize"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// ClusterKey is the key under _cluster that carries the table.
const ClusterKey = "image-overrides"

// DefaultSecret is the platform-wide copy of the values the table is read
// from when a component is not told otherwise.
var DefaultSecret = types.NamespacedName{Namespace: "cozy-system", Name: "cozystack-values"}

var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Parse reads the table from the values.yaml payload of a cozystack-values
// Secret. A payload without the key yields no overrides.
func Parse(valuesYAML []byte) ([]kustomize.Image, error) {
	var values struct {
		Cluster map[string]any `json:"_cluster"`
	}
	if err := yaml.Unmarshal(valuesYAML, &values); err != nil {
		return nil, fmt.Errorf("parsing values: %w", err)
	}
	raw, ok := values.Cluster[ClusterKey]
	if !ok || raw == nil {
		return nil, nil
	}
	// Round-trip through YAML so the table is decoded with the same field
	// names Flux uses for kustomize images, and an unknown field is an error
	// rather than an override that silently does nothing.
	encoded, err := yaml.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encoding %s: %w", ClusterKey, err)
	}
	var images []kustomize.Image
	if err := yaml.UnmarshalStrict(encoded, &images); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", ClusterKey, err)
	}
	if err := Validate(images); err != nil {
		return nil, err
	}
	return images, nil
}

// Validate rejects entries kustomize would apply wrongly or not at all.
func Validate(images []kustomize.Image) error {
	seen := make(map[string]bool, len(images))
	for i, img := range images {
		switch {
		case img.Name == "":
			return fmt.Errorf("%s[%d]: name is required", ClusterKey, i)
		case strings.Contains(img.Name, "@") || strings.Contains(img.Name[strings.LastIndex(img.Name, "/")+1:], ":"):
			// kustomize matches the name without tag or digest; a name that
			// carries one never matches anything.
			return fmt.Errorf("%s[%d]: name %q must not carry a tag or digest", ClusterKey, i, img.Name)
		case img.NewName == "" && img.NewTag == "" && img.Digest == "":
			return fmt.Errorf("%s[%d]: %q sets none of newName, newTag, digest", ClusterKey, i, img.Name)
		case img.Digest != "" && !digestRE.MatchString(img.Digest):
			return fmt.Errorf("%s[%d]: digest %q is not sha256:<64 hex>", ClusterKey, i, img.Digest)
		case seen[img.Name]:
			return fmt.Errorf("%s[%d]: %q is listed twice", ClusterKey, i, img.Name)
		}
		seen[img.Name] = true
	}
	return nil
}

// Load reads the table from the values.yaml of the given Secret. found is
// false when the Secret does not exist, which is not the same as an empty
// table: before the platform first creates the Secret no release carries
// overrides, but a Secret deleted later says nothing about the table, and a
// caller reading that as empty would strip the overrides from every release
// and send it to images an air-gapped cluster cannot pull. When found is
// false a caller keeps a release's post-renderers as they are.
func Load(ctx context.Context, c client.Reader, key types.NamespacedName) (images []kustomize.Image, found bool, err error) {
	secret := &corev1.Secret{}
	err = c.Get(ctx, key, secret)
	if apierrors.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading %s: %w", key, err)
	}
	images, err = Parse(secret.Data["values.yaml"])
	if err != nil {
		return nil, true, fmt.Errorf("%s: %w", key, err)
	}
	return images, true, nil
}

// PostRenderers returns the post-renderers a Go-built HelmRelease carries for
// the table: none when it is empty, otherwise one kustomize images renderer.
// Such a release has no post-renderers of its own, so the result replaces the
// field rather than being appended to it.
func PostRenderers(images []kustomize.Image) []helmv2.PostRenderer {
	if len(images) == 0 {
		return nil
	}
	return []helmv2.PostRenderer{{
		Kustomize: &helmv2.Kustomize{Images: append([]kustomize.Image(nil), images...)},
	}}
}

// Merge puts the renderer for images into post-renderers a release already
// has, for releases this code does not build from scratch. A renderer that
// carries images and nothing else is taken to be an earlier override renderer
// and is dropped; every other renderer is kept, in order, and the new one goes
// last so that it rewrites what they render. An empty table only drops.
func Merge(existing []helmv2.PostRenderer, images []kustomize.Image) []helmv2.PostRenderer {
	var merged []helmv2.PostRenderer
	for _, pr := range existing {
		if isOverrideRenderer(pr) {
			continue
		}
		merged = append(merged, pr)
	}
	return append(merged, PostRenderers(images)...)
}

func isOverrideRenderer(pr helmv2.PostRenderer) bool {
	return pr.Kustomize != nil && len(pr.Kustomize.Images) > 0 && len(pr.Kustomize.Patches) == 0
}
