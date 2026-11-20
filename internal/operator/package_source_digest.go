/*
Copyright 2026 The Cozystack Authors.

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
	"context"
	"encoding/json"
	"fmt"
	"strings"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	fluxmeta "github.com/fluxcd/pkg/apis/meta"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcewatcherv1beta1 "github.com/fluxcd/source-watcher/api/v2/v1beta1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	// AnnotationRenderOnSourceChange marks a PackageSource whose releases
	// must render again whenever its source moves, even when the charts
	// built from the new source are byte-identical. The platform chart is
	// one: it copies its own source into cozystack-packages at render time,
	// so a source that changes only system packages has to re-render it.
	AnnotationRenderOnSourceChange = "operator.cozystack.io/render-on-source-change"

	// observedDigestKey is written under sourceRef in the release values.
	// helm-controller upgrades a release when its values change, and only
	// then; no template has to read the key for it to do its job.
	observedDigestKey = "observedDigest"
)

type fluxSource interface {
	client.Object
	GetArtifact() *fluxmeta.Artifact
	GetConditions() []metav1.Condition
}

// settledSourcesDigest returns the value a following release is stamped
// with: the ArtifactGenerator's observed sources digest, followed by the
// generation of every source, once the digest describes what every source
// serves for its current spec. It returns "" while either side is still
// catching up.
//
// The order matters. The platform release's pre-upgrade migrations have to
// run before cozystack-packages moves, so the digest must not be the thing
// that renders the old chart against the new source. Its ExternalArtifact is
// rewritten before source-watcher records the new digest, so a digest that
// matches the sources means that chart is already in place. A source whose
// spec has changed but not been fetched still serves the old artifact, which
// the generator has also seen: the generation check keeps that from passing.
//
// The generations are part of the value because the platform copies the
// source spec, not its artifact. A move to a mirror serving the same
// artifact, or a rotated pull secret, leaves the digest as it was and would
// otherwise never reach cozystack-packages.
func (r *PackageReconciler) settledSourcesDigest(ctx context.Context, agName string) (string, error) {
	ag := &sourcewatcherv1beta1.ArtifactGenerator{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: "cozy-system", Name: agName}, ag); err != nil {
		return "", client.IgnoreNotFound(err)
	}
	if ag.Status.ObservedSourcesDigest == "" {
		return "", nil
	}

	observed := make(map[string]sourcewatcherv1beta1.ObservedSource, len(ag.Spec.Sources))
	generations := make([]string, 0, len(ag.Spec.Sources))
	for _, ref := range ag.Spec.Sources {
		var src fluxSource
		switch ref.Kind {
		case sourcev1.OCIRepositoryKind:
			src = &sourcev1.OCIRepository{}
		case sourcev1.GitRepositoryKind:
			src = &sourcev1.GitRepository{}
		default:
			return "", nil
		}
		namespace := ref.Namespace
		if namespace == "" {
			namespace = ag.Namespace
		}
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, src); err != nil {
			return "", client.IgnoreNotFound(err)
		}
		ready := apimeta.FindStatusCondition(src.GetConditions(), "Ready")
		if ready == nil || ready.Status != metav1.ConditionTrue || ready.ObservedGeneration != src.GetGeneration() {
			return "", nil
		}
		artifact := src.GetArtifact()
		if artifact == nil {
			return "", nil
		}
		// The same fields source-watcher hashes, so the digest can be
		// compared rather than re-derived from timestamps.
		o := sourcewatcherv1beta1.ObservedSource{
			Digest:   artifact.Digest,
			Revision: artifact.Revision,
			URL:      artifact.URL,
		}
		if origin, ok := artifact.Metadata[sourcewatcherv1beta1.ArtifactOriginRevisionAnnotation]; ok {
			o.OriginRevision = origin
		}
		observed[ref.Alias] = o
		generations = append(generations, fmt.Sprintf("%s=%d", ref.Alias, src.GetGeneration()))
	}

	if sourcewatcherv1beta1.HashObservedSources(observed) != ag.Status.ObservedSourcesDigest {
		return "", nil
	}
	return ag.Status.ObservedSourcesDigest + "@" + strings.Join(generations, ","), nil
}

// sourceDigestValues returns the release values with sourceRef.observedDigest
// set to the settled stamp. While the sources are not settled it carries
// over the stamp the release already has, so the stamp never triggers a
// render before the chart is rebuilt. It cannot stop a render that another
// values change triggers in the meantime; that render copies the new source
// spec with the chart that is still in place, as it did before the stamp.
func (r *PackageReconciler) sourceDigestValues(ctx context.Context, hr *helmv2.HelmRelease, agName string) (*apiextensionsv1.JSON, error) {
	digest, err := r.settledSourcesDigest(ctx, agName)
	if err != nil {
		return nil, err
	}
	if digest == "" {
		existing := &helmv2.HelmRelease{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(hr), existing); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return nil, err
			}
		} else if digest, err = observedDigestOf(existing.Spec.Values); err != nil {
			return nil, err
		}
	}
	if digest == "" {
		return hr.Spec.Values, nil
	}
	return withObservedDigest(hr.Spec.Values, digest)
}

func decodeValues(values *apiextensionsv1.JSON) (map[string]any, error) {
	out := map[string]any{}
	if values == nil || len(values.Raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(values.Raw, &out); err != nil {
		return nil, fmt.Errorf("release values are not an object: %w", err)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func observedDigestOf(values *apiextensionsv1.JSON) (string, error) {
	m, err := decodeValues(values)
	if err != nil {
		return "", err
	}
	ref, _ := m["sourceRef"].(map[string]any)
	digest, _ := ref[observedDigestKey].(string)
	return digest, nil
}

func withObservedDigest(values *apiextensionsv1.JSON, digest string) (*apiextensionsv1.JSON, error) {
	m, err := decodeValues(values)
	if err != nil {
		return nil, err
	}
	ref, ok := m["sourceRef"].(map[string]any)
	if !ok {
		if _, set := m["sourceRef"]; set {
			return nil, fmt.Errorf("release values: sourceRef is not an object")
		}
		ref = map[string]any{}
	}
	ref[observedDigestKey] = digest
	m["sourceRef"] = ref
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return &apiextensionsv1.JSON{Raw: raw}, nil
}

// observedSourcesDigestChanged lets through only the ArtifactGenerator
// updates that record a new build; its other status writes cannot change
// the digest a release is stamped with.
var observedSourcesDigestChanged = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldAG, okOld := e.ObjectOld.(*sourcewatcherv1beta1.ArtifactGenerator)
		newAG, okNew := e.ObjectNew.(*sourcewatcherv1beta1.ArtifactGenerator)
		if !okOld || !okNew {
			return false
		}
		return oldAG.Status.ObservedSourcesDigest != newAG.Status.ObservedSourcesDigest
	},
}

// packageForArtifactGenerator maps a generator to the Package of the same
// name, if its PackageSource asks to follow the source. The generator, its
// PackageSource and the Package share one name.
func (r *PackageReconciler) packageForArtifactGenerator(ctx context.Context, obj client.Object) []reconcile.Request {
	ps := &cozyv1alpha1.PackageSource{}
	if err := r.Get(ctx, types.NamespacedName{Name: obj.GetName()}, ps); err != nil {
		return nil
	}
	if ps.GetAnnotations()[AnnotationRenderOnSourceChange] != "true" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: obj.GetName()}}}
}

// sourceGenerationFetched lets through the source updates that finish
// fetching a new spec. One that changes the artifact also changes the
// generator's digest; one that does not, such as a mirror move or a rotated
// secret, reaches a following release only through this watch.
var sourceGenerationFetched = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		return readyGeneration(e.ObjectOld) != readyGeneration(e.ObjectNew)
	},
}

func readyGeneration(obj client.Object) int64 {
	src, ok := obj.(fluxSource)
	if !ok {
		return 0
	}
	ready := apimeta.FindStatusCondition(src.GetConditions(), "Ready")
	if ready == nil || ready.Status != metav1.ConditionTrue {
		return 0
	}
	return ready.ObservedGeneration
}

// packagesForSource maps a source to the following Packages whose generator
// builds from it.
func (r *PackageReconciler) packagesForSource(kind string) func(context.Context, client.Object) []reconcile.Request {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		ags := &sourcewatcherv1beta1.ArtifactGeneratorList{}
		if err := r.List(ctx, ags, client.InNamespace("cozy-system")); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for i := range ags.Items {
			ag := &ags.Items[i]
			for _, ref := range ag.Spec.Sources {
				namespace := ref.Namespace
				if namespace == "" {
					namespace = ag.Namespace
				}
				if ref.Kind == kind && ref.Name == obj.GetName() && namespace == obj.GetNamespace() {
					requests = append(requests, r.packageForArtifactGenerator(ctx, ag)...)
					break
				}
			}
		}
		return requests
	}
}
