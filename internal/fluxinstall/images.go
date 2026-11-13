package fluxinstall

import (
	"fmt"

	"github.com/fluxcd/pkg/apis/kustomize"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/kustomize/api/filters/imagetag"
	kustypes "sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/yaml"
)

// applyImageOverrides rewrites container images in the Flux manifests with
// the same kustomize filter helm-controller runs for a HelmRelease images
// post-renderer, so an entry copied from registries.imageOverrides behaves
// identically here.
func applyImageOverrides(objects []*unstructured.Unstructured, images []kustomize.Image) error {
	if len(images) == 0 {
		return nil
	}
	for _, obj := range objects {
		node, err := yaml.FromMap(obj.Object)
		if err != nil {
			return fmt.Errorf("converting %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
		for _, image := range images {
			// Same field mapping as helm-controller's adaptImages.
			filter := imagetag.LegacyFilter{ImageTag: kustypes.Image{
				Name:    image.Name,
				NewName: image.NewName,
				NewTag:  image.NewTag,
				Digest:  image.Digest,
			}}
			if _, err := filter.Filter([]*yaml.RNode{node}); err != nil {
				return fmt.Errorf("overriding images in %s %s: %w", obj.GetKind(), obj.GetName(), err)
			}
		}
		data, err := node.MarshalJSON()
		if err != nil {
			return fmt.Errorf("converting %s %s back: %w", obj.GetKind(), obj.GetName(), err)
		}
		if err := obj.UnmarshalJSON(data); err != nil {
			return fmt.Errorf("converting %s %s back: %w", obj.GetKind(), obj.GetName(), err)
		}
	}
	return nil
}
