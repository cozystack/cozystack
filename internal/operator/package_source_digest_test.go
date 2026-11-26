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
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	fluxmeta "github.com/fluxcd/pkg/apis/meta"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcewatcherv1beta1 "github.com/fluxcd/source-watcher/api/v2/v1beta1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

const (
	platformPkg    = "cozystack.cozystack-platform"
	platformSource = "cozystack-platform"
)

func sourceDigestScheme() *runtime.Scheme {
	s := privScheme()
	utilruntime.Must(sourcev1.AddToScheme(s))
	utilruntime.Must(sourcewatcherv1beta1.AddToScheme(s))
	return s
}

func platformPS(follow bool) *cozyv1alpha1.PackageSource {
	ps := &cozyv1alpha1.PackageSource{
		ObjectMeta: metav1.ObjectMeta{Name: platformPkg, Annotations: map[string]string{
			AnnotationSkipCozystackValues: "true",
		}},
		Spec: cozyv1alpha1.PackageSourceSpec{
			SourceRef: &cozyv1alpha1.PackageSourceRef{Kind: "OCIRepository", Name: platformSource, Namespace: "cozy-system", Path: "/"},
			Variants: []cozyv1alpha1.Variant{{
				Name: "default",
				Components: []cozyv1alpha1.Component{{Name: "platform", Path: "core/platform",
					Install: &cozyv1alpha1.ComponentInstall{Namespace: "cozy-system", ReleaseName: "cozystack-platform"}}},
			}},
		},
	}
	if follow {
		ps.Annotations[AnnotationRenderOnSourceChange] = "true"
	}
	return ps
}

func platformPackage(values string) *cozyv1alpha1.Package {
	pkg := &cozyv1alpha1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: platformPkg},
		Spec:       cozyv1alpha1.PackageSpec{Variant: "default"},
	}
	if values != "" {
		pkg.Spec.Components = map[string]cozyv1alpha1.PackageComponent{
			"platform": {Values: &apiextensionsv1.JSON{Raw: []byte(values)}},
		}
	}
	return pkg
}

// artifactOf is what source-controller serves for a packages digest.
func artifactOf(digest string) *fluxmeta.Artifact {
	return &fluxmeta.Artifact{
		Digest:   "sha256:content-" + digest,
		Revision: "sha256:" + digest,
		URL:      "http://flux.cozy-fluxcd.svc/ocirepository/cozy-system/cozystack-platform/sha256:" + digest + ".tar.gz",
		Metadata: map[string]string{sourcewatcherv1beta1.ArtifactOriginRevisionAnnotation: "v1.0.0@sha1:" + digest},
	}
}

// platformRepo is the platform source with the generation it has been
// asked for and the generation (and artifact) it has fetched.
func platformRepo(generation, fetched int64, digest string) *sourcev1.OCIRepository {
	return &sourcev1.OCIRepository{
		ObjectMeta: metav1.ObjectMeta{Name: platformSource, Namespace: "cozy-system", Generation: generation},
		Status: sourcev1.OCIRepositoryStatus{
			ObservedGeneration: fetched,
			Artifact:           artifactOf(digest),
			Conditions: []metav1.Condition{{
				Type: "Ready", Status: metav1.ConditionTrue, Reason: "Succeeded", ObservedGeneration: fetched,
			}},
		},
	}
}

// hashFor is the digest source-watcher records once it has built from the
// artifact served for digest.
func hashFor(digest string) string {
	a := artifactOf(digest)
	return sourcewatcherv1beta1.HashObservedSources(map[string]sourcewatcherv1beta1.ObservedSource{
		platformSource: {Digest: a.Digest, Revision: a.Revision, URL: a.URL, OriginRevision: a.Metadata[sourcewatcherv1beta1.ArtifactOriginRevisionAnnotation]},
	})
}

// platformAG is the generator after it last built from the artifact served
// for digest.
func platformAG(builtFrom string) *sourcewatcherv1beta1.ArtifactGenerator {
	return &sourcewatcherv1beta1.ArtifactGenerator{
		ObjectMeta: metav1.ObjectMeta{Name: platformPkg, Namespace: "cozy-system"},
		Spec: sourcewatcherv1beta1.ArtifactGeneratorSpec{
			Sources: []sourcewatcherv1beta1.SourceReference{{Alias: platformSource, Kind: "OCIRepository", Name: platformSource, Namespace: "cozy-system"}},
		},
		Status: sourcewatcherv1beta1.ArtifactGeneratorStatus{ObservedSourcesDigest: hashFor(builtFrom)},
	}
}

// stampFor is the value a following release gets once the source, asked for
// generation, serves the artifact for digest and the chart is built from it.
func stampFor(digest string, generation int64) string {
	return fmt.Sprintf("%s@%s=%d", hashFor(digest), platformSource, generation)
}

func reconcilePlatform(t *testing.T, cl client.Client) map[string]any {
	t.Helper()
	r := &PackageReconciler{Client: cl, Scheme: sourceDigestScheme()}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: platformPkg}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	hr := &helmv2.HelmRelease{}
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: "cozy-system", Name: "cozystack-platform"}, hr); err != nil {
		t.Fatalf("platform HelmRelease: %v", err)
	}
	values := map[string]any{}
	if hr.Spec.Values != nil {
		if err := json.Unmarshal(hr.Spec.Values.Raw, &values); err != nil {
			t.Fatal(err)
		}
	}
	return values
}

func observedDigest(values map[string]any) string {
	ref, _ := values["sourceRef"].(map[string]any)
	d, _ := ref[observedDigestKey].(string)
	return d
}

func newPlatformClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(sourceDigestScheme()).WithObjects(objs...).
		WithStatusSubresource(&cozyv1alpha1.Package{}).Build()
}

// A new packages artifact that leaves core/platform byte-identical produces
// the same platform chart, so only a values change can make helm-controller
// render the platform again and move cozystack-packages along.
func TestPlatformReleaseRendersAgainWhenOnlyTheSourceMoves(t *testing.T) {
	ctx := context.Background()
	cl := newPlatformClient(platformPS(true), platformPackage(""), platformRepo(1, 1, "aaaa"), platformAG("aaaa"))
	if got := observedDigest(reconcilePlatform(t, cl)); got != stampFor("aaaa", 1) {
		t.Fatalf("observedDigest = %q, want the settled stamp %q", got, stampFor("aaaa", 1))
	}

	// The installer points the source at a new artifact; source-controller
	// fetches it and source-watcher rebuilds the (identical) platform chart.
	repo := &sourcev1.OCIRepository{}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "cozy-system", Name: platformSource}, repo); err != nil {
		t.Fatal(err)
	}
	moved := platformRepo(2, 2, "bbbb")
	repo.Generation, repo.Status = moved.Generation, moved.Status
	if err := cl.Update(ctx, repo); err != nil {
		t.Fatal(err)
	}
	ag := &sourcewatcherv1beta1.ArtifactGenerator{}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "cozy-system", Name: platformPkg}, ag); err != nil {
		t.Fatal(err)
	}
	ag.Status.ObservedSourcesDigest = hashFor("bbbb")
	if err := cl.Update(ctx, ag); err != nil {
		t.Fatal(err)
	}

	if got := observedDigest(reconcilePlatform(t, cl)); got != stampFor("bbbb", 2) {
		t.Fatalf("observedDigest = %q after the source moved, want %q: the platform release would not render again", got, stampFor("bbbb", 2))
	}
}

// The platform's pre-upgrade migrations must run before cozystack-packages
// moves, so the release may only render again with the chart built from the
// new source. Until source-watcher has built it, the digest stays where it
// was, even across an unrelated values edit.
func TestPlatformReleaseHoldsUntilTheChartIsBuiltFromTheNewSource(t *testing.T) {
	cases := []struct {
		name string
		repo *sourcev1.OCIRepository
		ag   *sourcewatcherv1beta1.ArtifactGenerator
	}{
		{name: "source spec changed, not fetched yet", repo: platformRepo(2, 1, "aaaa"), ag: platformAG("aaaa")},
		{name: "source fetched, chart not rebuilt yet", repo: platformRepo(2, 2, "bbbb"), ag: platformAG("aaaa")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			existing := &helmv2.HelmRelease{
				ObjectMeta: metav1.ObjectMeta{Name: "cozystack-platform", Namespace: "cozy-system"},
				Spec: helmv2.HelmReleaseSpec{Values: &apiextensionsv1.JSON{Raw: []byte(
					`{"sourceRef":{"observedDigest":"sha256:previous"}}`)}},
			}
			cl := newPlatformClient(platformPS(true), platformPackage(`{"publishing":{"host":"edited"}}`), tc.repo, tc.ag, existing)
			values := reconcilePlatform(t, cl)
			if got := observedDigest(values); got != "sha256:previous" {
				t.Fatalf("observedDigest = %q, want it held at sha256:previous", got)
			}
			if pub, _ := values["publishing"].(map[string]any); pub["host"] != "edited" {
				t.Errorf("Package values were lost: %v", values)
			}
		})
	}
}

// The release that ships this change also moves the source. Introducing the
// key before the new chart is built would render the old chart against the
// new source, so a release with no digest yet must not get one early.
func TestPlatformReleaseGetsNoDigestBeforeTheSourceSettles(t *testing.T) {
	existing := &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Name: "cozystack-platform", Namespace: "cozy-system"}}
	cl := newPlatformClient(platformPS(true), platformPackage(""), platformRepo(2, 1, "aaaa"), platformAG("aaaa"), existing)
	if values := reconcilePlatform(t, cl); values["sourceRef"] != nil {
		t.Fatalf("values = %v, want no sourceRef while the source is unsettled", values)
	}
}

func TestPlatformReleaseKeepsItsSourceRefValues(t *testing.T) {
	cl := newPlatformClient(platformPS(true), platformPackage(`{"sourceRef":{"name":"custom"}}`), platformRepo(1, 1, "aaaa"), platformAG("aaaa"))
	ref, _ := reconcilePlatform(t, cl)["sourceRef"].(map[string]any)
	if ref["name"] != "custom" || ref[observedDigestKey] != stampFor("aaaa", 1) {
		t.Fatalf("sourceRef = %v, want name kept and the digest added", ref)
	}
}

// Every other package's chart already changes with its content; stamping
// the digest there would upgrade every release on every artifact bump.
func TestPackagesWithoutTheAnnotationAreNotStamped(t *testing.T) {
	cl := newPlatformClient(platformPS(false), platformPackage(""), platformRepo(1, 1, "aaaa"), platformAG("aaaa"))
	if values := reconcilePlatform(t, cl); values["sourceRef"] != nil {
		t.Fatalf("values = %v, want nothing stamped without the annotation", values)
	}
}

func TestArtifactGeneratorEventsReachOnlyFollowingPackages(t *testing.T) {
	ag := platformAG("aaaa")
	for _, follow := range []bool{true, false} {
		cl := newPlatformClient(platformPS(follow), platformPackage(""))
		r := &PackageReconciler{Client: cl, Scheme: sourceDigestScheme()}
		got := r.packageForArtifactGenerator(context.Background(), ag)
		if follow && (len(got) != 1 || got[0].Name != platformPkg) {
			t.Errorf("following PackageSource: requests = %v, want the platform Package", got)
		}
		if !follow && len(got) != 0 {
			t.Errorf("non-following PackageSource: requests = %v, want none", got)
		}
	}
}

// The platform copies the source spec into cozystack-packages. A move to a
// mirror that serves the same artifact, or a rotated pull secret, leaves the
// artifact and the generator's digest as they were, and still has to reach
// cozystack-packages.
func TestPlatformReleaseRendersAgainWhenOnlyTheSourceSpecChanges(t *testing.T) {
	ctx := context.Background()
	cl := newPlatformClient(platformPS(true), platformPackage(""), platformRepo(1, 1, "aaaa"), platformAG("aaaa"))
	before := observedDigest(reconcilePlatform(t, cl))

	repo := &sourcev1.OCIRepository{}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "cozy-system", Name: platformSource}, repo); err != nil {
		t.Fatal(err)
	}
	mirrored := platformRepo(2, 2, "aaaa")
	repo.Generation, repo.Status = mirrored.Generation, mirrored.Status
	if err := cl.Update(ctx, repo); err != nil {
		t.Fatal(err)
	}

	after := observedDigest(reconcilePlatform(t, cl))
	if after == before {
		t.Fatalf("observedDigest stayed %q after the source spec changed: cozystack-packages would keep the old spec", after)
	}
	if after != stampFor("aaaa", 2) {
		t.Fatalf("observedDigest = %q, want %q", after, stampFor("aaaa", 2))
	}
}

// Every Package value of a following release is decoded and encoded again
// to add the stamp; a large integer must come out as it went in.
func TestPlatformReleaseKeepsLargeIntegersExact(t *testing.T) {
	cl := newPlatformClient(platformPS(true), platformPackage(`{"limit":9007199254740993}`), platformRepo(1, 1, "aaaa"), platformAG("aaaa"))
	r := &PackageReconciler{Client: cl, Scheme: sourceDigestScheme()}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: platformPkg}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	hr := &helmv2.HelmRelease{}
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: "cozy-system", Name: "cozystack-platform"}, hr); err != nil {
		t.Fatal(err)
	}
	var values struct {
		Limit json.Number `json:"limit"`
	}
	dec := json.NewDecoder(strings.NewReader(string(hr.Spec.Values.Raw)))
	dec.UseNumber()
	if err := dec.Decode(&values); err != nil {
		t.Fatal(err)
	}
	if values.Limit != "9007199254740993" {
		t.Fatalf("limit = %s, want 9007199254740993", values.Limit)
	}
}

func TestSourceEventsReachOnlyFollowingPackagesBuiltFromThem(t *testing.T) {
	other := &sourcev1.OCIRepository{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "cozy-system"}}
	for _, follow := range []bool{true, false} {
		cl := newPlatformClient(platformPS(follow), platformPackage(""), platformAG("aaaa"))
		r := &PackageReconciler{Client: cl, Scheme: sourceDigestScheme()}
		mapper := r.packagesForSource(sourcev1.OCIRepositoryKind)
		got := mapper(context.Background(), platformRepo(1, 1, "aaaa"))
		if follow && (len(got) != 1 || got[0].Name != platformPkg) {
			t.Errorf("following PackageSource: requests = %v, want the platform Package", got)
		}
		if !follow && len(got) != 0 {
			t.Errorf("non-following PackageSource: requests = %v, want none", got)
		}
		if got := mapper(context.Background(), other); len(got) != 0 {
			t.Errorf("unrelated source: requests = %v, want none", got)
		}
		if got := r.packagesForSource(sourcev1.GitRepositoryKind)(context.Background(), platformRepo(1, 1, "aaaa")); len(got) != 0 {
			t.Errorf("same name, other kind: requests = %v, want none", got)
		}
	}
}

func TestSourceWatchFiresWhenANewSpecIsFetched(t *testing.T) {
	update := func(oldObj, newObj client.Object) bool {
		return sourceGenerationFetched.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj})
	}
	if update(platformRepo(1, 1, "aaaa"), platformRepo(2, 1, "aaaa")) {
		t.Error("a spec change not yet fetched must not fire: nothing has settled")
	}
	if !update(platformRepo(2, 1, "aaaa"), platformRepo(2, 2, "aaaa")) {
		t.Error("fetching the new spec must fire even when the artifact is the same")
	}
	if update(platformRepo(2, 2, "aaaa"), platformRepo(2, 2, "aaaa")) {
		t.Error("a status write that fetches nothing new must not fire")
	}
}
