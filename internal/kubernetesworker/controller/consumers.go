package controller

import (
	"context"
	"errors"
	"fmt"
	"path"
	"reflect"
	"strings"

	packages "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/marketplace/naming"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	watcherv1 "github.com/fluxcd/source-watcher/api/v2/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The chart artifacts pool releases are installed from; the application
// definition and the computeplane chart name them.
const (
	applicationPoolArtifact  = "cozystack-kubernetes-nodes-application-kubevirt-kubernetes-nodes"
	computeplanePoolArtifact = "cozystack-computeplane-application-kubevirt-kubernetes-nodes"
)

var errUnknownConsumer = errors.New("cannot identify HelmRelease pool consumer; verify package provenance before deleting WorkerClass")

// unknownConsumer keeps the read error that hid the provenance, so a Forbidden
// or a timeout is not reported as a chart source that could not be traced.
func unknownConsumer(err error) error {
	if err == nil {
		return errUnknownConsumer
	}
	return fmt.Errorf("%w: %w", errUnknownConsumer, err)
}

func (r *Reconciler) poolConsumer(ctx context.Context, hr *helmv2.HelmRelease) (bool, error) {
	ref := hr.Spec.ChartRef
	if ref == nil || ref.Kind != "ExternalArtifact" || ref.Namespace != "cozy-system" {
		return false, errUnknownConsumer
	}
	owner := metav1.GetControllerOf(hr)
	canonical := ref.Name == applicationPoolArtifact || ref.Name == computeplanePoolArtifact
	ea := &sourcev1.ExternalArtifact{}
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, ea); err != nil {
		if canonical && owner == nil && apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, unknownConsumer(err)
	}
	source := ea.Spec.SourceRef
	generatorUID := ea.Labels[watcherv1.ArtifactGeneratorLabel]
	if source == nil || source.APIVersion != watcherv1.GroupVersion.String() || source.Kind != watcherv1.ArtifactGeneratorKind || source.Name == "" || source.Namespace != ea.Namespace || generatorUID == "" {
		return false, errUnknownConsumer
	}
	ag := &watcherv1.ArtifactGenerator{}
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: source.Namespace, Name: source.Name}, ag); err != nil || string(ag.UID) != generatorUID {
		return false, unknownConsumer(err)
	}
	agOwner := metav1.GetControllerOf(ag)
	if agOwner == nil || agOwner.APIVersion != packages.GroupVersion.String() || agOwner.Kind != "PackageSource" || agOwner.Name != ag.Name || agOwner.UID == "" {
		return false, errUnknownConsumer
	}
	ps := &packages.PackageSource{}
	if err := r.Reader.Get(ctx, client.ObjectKey{Name: agOwner.Name}, ps); err != nil || ps.UID != agOwner.UID {
		return false, unknownConsumer(err)
	}
	src := ps.Spec.SourceRef
	if !strings.HasPrefix(ps.Name, "cozystack.") || src == nil || src.Namespace != "cozy-system" || src.Path != "/" {
		return false, errUnknownConsumer
	}
	packageSource := src.Kind == "OCIRepository" && src.Name == "cozystack-packages"
	platformSource := owner != nil && src.Name != "" && ps.Name == "cozystack."+src.Name && (src.Kind == "OCIRepository" || src.Kind == "GitRepository")
	if !packageSource && !platformSource {
		return false, errUnknownConsumer
	}
	pkg := &packages.Package{}
	if err := r.Reader.Get(ctx, client.ObjectKey{Name: ps.Name}, pkg); err != nil || pkg.UID == "" {
		return false, unknownConsumer(err)
	}
	if owner != nil && (owner.APIVersion != packages.GroupVersion.String() || owner.Kind != "Package" || owner.Name != pkg.Name || owner.UID != pkg.UID) {
		return false, errUnknownConsumer
	}
	if len(ag.Spec.Sources) != 1 || ag.Spec.Sources[0] != (watcherv1.SourceReference{Alias: src.Name, Kind: src.Kind, Name: src.Name, Namespace: src.Namespace}) {
		return false, errUnknownConsumer
	}
	variantName := pkg.Spec.Variant
	if variantName == "" {
		variantName = "default"
	}
	for _, variant := range ps.Spec.Variants {
		if variant.Name != variantName {
			continue
		}
		for _, component := range variant.Components {
			if naming.ArtifactName(ps.Name, variant.Name, component.Name) != ea.Name {
				continue
			}
			if !packageSource && (component.Name != "platform" || component.Path != "core/platform" || component.Install == nil || component.Install.Namespace != "cozy-system" || component.Install.ReleaseName != "cozystack-platform") {
				return false, errUnknownConsumer
			}
			parts := strings.Split(component.Path, "/")
			if len(parts) != 2 || path.Clean(component.Path) != component.Path || parts[1] == "" || (parts[0] != "apps" && parts[0] != "system" && parts[0] != "extra" && parts[0] != "core") {
				return false, errUnknownConsumer
			}
			if owner != nil {
				if component.Install == nil || component.Install.Namespace != hr.Namespace {
					return false, errUnknownConsumer
				}
				releaseName := component.Install.ReleaseName
				if releaseName == "" {
					releaseName = component.Name
				}
				if hr.Name != releaseName {
					return false, errUnknownConsumer
				}
			}
			if override, ok := pkg.Spec.Components[component.Name]; ok && override.Enabled != nil && !*override.Enabled {
				return false, errUnknownConsumer
			}
			copies := []watcherv1.CopyOperation{{From: "@" + src.Name + "/" + component.Path + "/**", To: "@artifact/" + path.Base(component.Path) + "/"}}
			for _, name := range component.Libraries {
				found := false
				for _, library := range variant.Libraries {
					if library.Name == name {
						if name != "cozy-lib" || library.Path != "library/cozy-lib" {
							return false, errUnknownConsumer
						}
						copies = append(copies, watcherv1.CopyOperation{From: "@" + src.Name + "/" + library.Path + "/**", To: "@artifact/" + path.Base(component.Path) + "/charts/" + name + "/"})
						found = true
					}
				}
				if !found {
					return false, errUnknownConsumer
				}
			}
			for i, file := range component.ValuesFiles {
				strategy := "Merge"
				if i == 0 {
					strategy = "Overwrite"
				}
				copies = append(copies, watcherv1.CopyOperation{From: "@" + src.Name + "/" + component.Path + "/" + file, To: "@artifact/" + path.Base(component.Path) + "/values.yaml", Strategy: strategy})
			}
			var matched *watcherv1.OutputArtifact
			for i := range ag.Spec.OutputArtifacts {
				if ag.Spec.OutputArtifacts[i].Name != ea.Name {
					continue
				}
				if matched != nil {
					return false, errUnknownConsumer
				}
				matched = &ag.Spec.OutputArtifacts[i]
			}
			if matched != nil && reflect.DeepEqual(matched.Copy, copies) {
				if canonical && component.Path != "apps/kubernetes-nodes" {
					return false, errUnknownConsumer
				}
				return component.Path == "apps/kubernetes-nodes", nil
			}
		}
	}
	return false, errUnknownConsumer
}
