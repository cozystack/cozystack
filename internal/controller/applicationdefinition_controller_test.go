package controller

import (
	"context"
	"testing"
	"time"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func newAppDefScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := cozyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cozyv1alpha1: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add appsv1: %v", err)
	}
	return scheme
}

// TestApplicationDefinition_NoEventsIsNoop pins the early-exit path:
// when no event has been recorded yet (lastEvent zero), Reconcile must
// return an empty Result without touching the deployment or computing
// hashes.
func TestApplicationDefinition_NoEventsIsNoop(t *testing.T) {
	scheme := newAppDefScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &ApplicationDefinitionReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Debounce: 5 * time.Second,
	}

	res, err := r.Reconcile(context.TODO(), reconcile.Request{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected empty result, got %+v", res)
	}
}

// TestApplicationDefinition_WithinDebounceRequeues pins the debounce-window
// path: an event was just recorded, within the debounce window. Reconcile
// must return a RequeueAfter equal to the remaining wait, no work done.
func TestApplicationDefinition_WithinDebounceRequeues(t *testing.T) {
	scheme := newAppDefScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &ApplicationDefinitionReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Debounce: 5 * time.Second,
	}
	r.lastEvent = time.Now()

	res, err := r.Reconcile(context.TODO(), reconcile.Request{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("expected positive RequeueAfter, got %+v", res)
	}
	if res.RequeueAfter > 5*time.Second {
		t.Fatalf("RequeueAfter exceeds debounce window: %v", res.RequeueAfter)
	}
}

// TestApplicationDefinition_AlreadyHandledIsNoop pins the lastHandled
// gate: lastHandled >= lastEvent means this event has already been
// processed; Reconcile is a no-op.
func TestApplicationDefinition_AlreadyHandledIsNoop(t *testing.T) {
	scheme := newAppDefScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	now := time.Now().Add(-10 * time.Second)
	r := &ApplicationDefinitionReconciler{
		Client:      fakeClient,
		Scheme:      scheme,
		Debounce:    5 * time.Second,
		lastEvent:   now,
		lastHandled: now,
	}

	res, err := r.Reconcile(context.TODO(), reconcile.Request{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected empty result, got %+v", res)
	}
}

// TestApplicationDefinition_MissingDeploymentIgnored pins the
// IgnoreNotFound path: when the cozystack-api Deployment is absent
// (initial install in progress, etc.), Reconcile must not return an
// error.
func TestApplicationDefinition_MissingDeploymentIgnored(t *testing.T) {
	scheme := newAppDefScheme(t)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &ApplicationDefinitionReconciler{
		Client:    fakeClient,
		Scheme:    scheme,
		Debounce:  5 * time.Second,
		lastEvent: time.Now().Add(-10 * time.Second),
	}

	res, err := r.Reconcile(context.TODO(), reconcile.Request{})
	if err != nil {
		t.Fatalf("expected nil error on missing deployment, got %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected empty result, got %+v", res)
	}
}

// TestApplicationDefinition_HashChangePatchesDeployment pins the happy
// path: an event has elapsed past debounce, the deployment exists with
// either no hash annotation or a stale hash, Reconcile must patch the
// pod template's cozystack.io/config-hash annotation.
func TestApplicationDefinition_HashChangePatchesDeployment(t *testing.T) {
	scheme := newAppDefScheme(t)

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cozystack-api",
			Namespace: "cozy-system",
		},
	}
	appDef := &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "harbor"},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(dep, appDef).
		Build()

	r := &ApplicationDefinitionReconciler{
		Client:    fakeClient,
		Scheme:    scheme,
		Debounce:  5 * time.Second,
		lastEvent: time.Now().Add(-10 * time.Second),
	}

	if _, err := r.Reconcile(context.TODO(), reconcile.Request{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &appsv1.Deployment{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{Name: "cozystack-api", Namespace: "cozy-system"}, got); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	hash, ok := got.Spec.Template.Annotations["cozystack.io/config-hash"]
	if !ok || hash == "" {
		t.Fatalf("expected config-hash annotation set, got %q (present=%v)", hash, ok)
	}
}

// TestApplicationDefinition_HashUnchangedSkipsPatch pins the no-op
// path: same hash already on the deployment means no patch is issued.
// We seed the deployment with the hash that the reconciler will compute
// and verify the lastHandled marker advances without changing the
// annotation.
func TestApplicationDefinition_HashUnchangedSkipsPatch(t *testing.T) {
	scheme := newAppDefScheme(t)

	appDef := &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "harbor"},
	}

	// First, run the reconciler against a deployment with empty annotation
	// to capture the hash it computes.
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cozystack-api",
			Namespace: "cozy-system",
		},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(dep, appDef).
		Build()
	r := &ApplicationDefinitionReconciler{
		Client:    fakeClient,
		Scheme:    scheme,
		Debounce:  5 * time.Second,
		lastEvent: time.Now().Add(-10 * time.Second),
	}
	if _, err := r.Reconcile(context.TODO(), reconcile.Request{}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	got := &appsv1.Deployment{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{Name: "cozystack-api", Namespace: "cozy-system"}, got); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	expectedHash := got.Spec.Template.Annotations["cozystack.io/config-hash"]

	// Now drive a second reconcile cycle with a fresh event but the same
	// state — hash will match, no patch should be needed. We can only
	// verify the no-op via lastHandled advancing; the patch itself is a
	// no-op so the annotation stays identical.
	r.lastEvent = time.Now().Add(-10 * time.Second)
	r.lastHandled = time.Time{}
	if _, err := r.Reconcile(context.TODO(), reconcile.Request{}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	again := &appsv1.Deployment{}
	if err := fakeClient.Get(context.TODO(), types.NamespacedName{Name: "cozystack-api", Namespace: "cozy-system"}, again); err != nil {
		t.Fatalf("get deployment after second reconcile: %v", err)
	}
	if again.Spec.Template.Annotations["cozystack.io/config-hash"] != expectedHash {
		t.Fatalf("hash mutated when it should have been stable: was %q now %q",
			expectedHash, again.Spec.Template.Annotations["cozystack.io/config-hash"])
	}
	if r.lastHandled.IsZero() {
		t.Fatalf("expected lastHandled advanced after no-op reconcile")
	}
}

// hashWithAnnotations computes the config hash for a single ApplicationDefinition
// carrying the given metadata annotations.
func hashWithAnnotations(t *testing.T, annotations map[string]string) string {
	t.Helper()

	scheme := newAppDefScheme(t)
	appDef := &cozyv1alpha1.ApplicationDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "vm-instance", Annotations: annotations},
	}
	r := &ApplicationDefinitionReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(appDef).Build(),
		Scheme: scheme,
	}

	hash, err := r.computeConfigHash(context.TODO())
	if err != nil {
		t.Fatalf("computeConfigHash: %v", err)
	}
	return hash
}

// TestApplicationDefinition_HashCoversReleaseAnnotations pins that a change
// confined to a release.cozystack.io/* annotation moves the hash. cozystack-api
// reads those annotations once, at start-up (pkg/cmd/server/start.go), so the
// config-hash rollout is the only thing that makes such a change take effect.
// A hash built from spec alone leaves the annotation inert on an existing
// cluster until the api pod happens to restart for an unrelated reason, which
// is indistinguishable from the change never having been delivered.
func TestApplicationDefinition_HashCoversReleaseAnnotations(t *testing.T) {
	bare := hashWithAnnotations(t, nil)
	annotated := hashWithAnnotations(t, map[string]string{
		config.HelmInstallDisableWaitAnnotation: "true",
	})

	if bare == annotated {
		t.Fatalf("hash ignores %s: %q for both", config.HelmInstallDisableWaitAnnotation, bare)
	}

	flipped := hashWithAnnotations(t, map[string]string{
		config.HelmInstallDisableWaitAnnotation: "false",
	})
	if flipped == annotated {
		t.Fatalf("hash ignores the annotation's value: %q for both true and false", annotated)
	}
}

// TestApplicationDefinition_HashIgnoresUnrelatedAnnotations pins the other half:
// only the release.cozystack.io/ prefix counts. kubectl and Flux rewrite their
// own annotations on every apply, and hashing those would roll out cozystack-api
// on every reconcile of every ApplicationDefinition.
func TestApplicationDefinition_HashIgnoresUnrelatedAnnotations(t *testing.T) {
	bare := hashWithAnnotations(t, nil)
	noisy := hashWithAnnotations(t, map[string]string{
		"kubectl.kubernetes.io/last-applied-configuration": `{"metadata":{"name":"vm-instance"}}`,
		"meta.helm.sh/release-name":                        "vm-instance-rd",
	})

	if bare != noisy {
		t.Fatalf("unrelated annotations moved the hash: %q vs %q", bare, noisy)
	}
}
