package backupcontroller

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

// gateGVKs are the kinds the gate touches in these tests: the
// strategy.backups.cozystack.io CRs cozy-default routes to, the Velero BSL,
// and the HelmRelease it patches. The RESTMapper and the dynamic fake's
// resource registry are both derived from this one list so the plural the
// gate resolves and the plural the fake serves cannot drift apart.
var gateGVKs = []schema.GroupVersionKind{
	{Group: strategyAPIGroup, Version: "v1alpha1", Kind: "CNPG"},
	{Group: strategyAPIGroup, Version: "v1alpha1", Kind: "MariaDB"},
	{Group: strategyAPIGroup, Version: "v1alpha1", Kind: "Etcd"},
	{Group: strategyAPIGroup, Version: "v1alpha1", Kind: "Altinity"},
	{Group: strategyAPIGroup, Version: "v1alpha1", Kind: "Velero"},
	{Group: "velero.io", Version: "v1", Kind: "BackupStorageLocation"},
	{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"},
}

func gateScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = backupsv1alpha1.AddToScheme(scheme)
	return scheme
}

func gateListKinds() map[schema.GroupVersionResource]string {
	out := map[schema.GroupVersionResource]string{}
	for _, gvk := range gateGVKs {
		gvr, _ := meta.UnsafeGuessKindToResource(gvk)
		out[gvr] = gvk.Kind + "List"
	}
	return out
}

// gateRESTMapper resolves group+kind (the only coordinates a
// BackupClass strategyRef carries) to a resource, which is what the gate
// does in production through the dynamic RESTMapper. The group versions must
// be passed as defaults: RESTMapping is called without a version.
func gateRESTMapper() meta.RESTMapper {
	groupVersions := map[schema.GroupVersion]struct{}{}
	for _, gvk := range gateGVKs {
		groupVersions[gvk.GroupVersion()] = struct{}{}
	}
	var gvs []schema.GroupVersion
	for gv := range groupVersions {
		gvs = append(gvs, gv)
	}
	m := meta.NewDefaultRESTMapper(gvs)
	for _, gvk := range gateGVKs {
		scope := meta.RESTScopeRoot
		if gvk.Kind == "BackupStorageLocation" || gvk.Kind == "HelmRelease" {
			scope = meta.RESTScopeNamespace
		}
		m.Add(gvk, scope)
	}
	return m
}

// cozyDefaultBackupClass mirrors the routes the chart's
// backupclass-default.yaml renders unconditionally: it is the manifest of
// what must exist, which is exactly why the gate reads it instead of
// hard-coding a list of Strategy names.
func cozyDefaultBackupClass() *backupsv1alpha1.BackupClass {
	ref := func(kind, name string) backupsv1alpha1.BackupClassStrategy {
		return backupsv1alpha1.BackupClassStrategy{
			Application: backupsv1alpha1.ApplicationSelector{
				APIGroup: new("apps.cozystack.io"),
				Kind:     kind + "App",
			},
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: new(strategyAPIGroup),
				Kind:     kind,
				Name:     name,
			},
		}
	}
	return &backupsv1alpha1.BackupClass{
		ObjectMeta: metav1.ObjectMeta{Name: "cozy-default"},
		Spec: backupsv1alpha1.BackupClassSpec{
			Strategies: []backupsv1alpha1.BackupClassStrategy{
				ref("CNPG", "cozy-default-cnpg"),
				ref("MariaDB", "cozy-default-mariadb"),
				ref("Etcd", "cozy-default-etcd"),
				ref("Altinity", "cozy-default-altinity"),
				ref("Velero", "cozy-default-velero-vminstance"),
				ref("Velero", "cozy-default-velero-vmdisk"),
			},
		},
	}
}

// renderedEndpoint is what the chart stamps on every default object for the
// endpoint sourceSecret carries: the steady state after a render that saw it.
const renderedEndpoint = "https://s3.example.com"

func strategyObject(kind, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(strategyAPIGroup + "/v1alpha1")
	u.SetKind(kind)
	u.SetName(name)
	u.SetAnnotations(map[string]string{endpointAnnotation: renderedEndpoint})
	return u
}

func withEndpoint(u *unstructured.Unstructured, endpoint string) *unstructured.Unstructured {
	u.SetAnnotations(map[string]string{endpointAnnotation: endpoint})
	return u
}

// unannotated is an object rendered by a chart that predates the endpoint
// annotation.
func unannotated(u *unstructured.Unstructured) *unstructured.Unstructured {
	u.SetAnnotations(nil)
	return u
}

// allDefaultObjects is every object cozy-default routes to, plus the BSL, as
// the chart renders them for sourceSecret's endpoint.
func allDefaultObjects() []runtime.Object {
	return []runtime.Object{
		strategyObject("CNPG", "cozy-default-cnpg"),
		strategyObject("MariaDB", "cozy-default-mariadb"),
		strategyObject("Etcd", "cozy-default-etcd"),
		strategyObject("Altinity", "cozy-default-altinity"),
		strategyObject("Velero", "cozy-default-velero-vminstance"),
		strategyObject("Velero", "cozy-default-velero-vmdisk"),
		bslObject(),
	}
}

// cosiSecret is the Secret the COSI sidecar writes for the platform bucket's
// BucketAccess, which user-credentials.yaml renders the credentials Secret
// from. The BucketInfo document is one read off a live cluster, with the keys
// and the host replaced.
func cosiSecret(endpoint string) *corev1.Secret {
	bucketInfo := `{"metadata": {"name": "bc-b8e30337-1821-418f-a4dd-163e8016aaee", "creationTimestamp": null}, ` +
		`"spec": {"bucketName": "bucket-1a2b", "authenticationType": "KEY", ` +
		`"secretS3": {"endpoint": "` + endpoint + `", "region": "", "accessKeyID": "AK", "accessSecretKey": "SK"}, ` +
		`"secretAzure": null, "protocols": ["s3"]}}`
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-root", Name: "bucket-cozy-backups-system"},
		Data:       map[string][]byte{"BucketInfo": []byte(bucketInfo)},
	}
}

func helmReleaseObject() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("helm.toolkit.fluxcd.io/v2")
	u.SetKind("HelmRelease")
	u.SetNamespace("cozy-backup-controller")
	u.SetName("backupstrategy-controller")
	return u
}

// credentialsHelmReleaseObject is the platform bucket's <bucket>-system
// release: the one that renders the user-credentials Secret the gate reads
// to resolve the bucket name, behind its own install-time lookup.
func credentialsHelmReleaseObject() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("helm.toolkit.fluxcd.io/v2")
	u.SetKind("HelmRelease")
	u.SetNamespace("tenant-root")
	u.SetName("bucket-cozy-backups-system")
	return u
}

func suspended(u *unstructured.Unstructured) *unstructured.Unstructured {
	_ = unstructured.SetNestedField(u.Object, true, "spec", "suspend")
	return u
}

func newGate(t *testing.T, ctrlObjs []client.Object, dynObjs ...runtime.Object) (*DefaultObjectsGate, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gateListKinds(), dynObjs...)
	g := &DefaultObjectsGate{
		Client: fake.NewClientBuilder().WithScheme(gateScheme()).WithObjects(ctrlObjs...).Build(),
		Config: BackupCredentialsConfig{
			SourceNamespace:  "tenant-root",
			SourceSecretName: "bucket-cozy-backups-system-credentials",
			TargetSecretName: "cozy-backups-creds",
		},
		BackupClassName: "cozy-default",
		HelmRelease: types.NamespacedName{
			Namespace: "cozy-backup-controller",
			Name:      "backupstrategy-controller",
		},
		CredentialsHelmRelease: types.NamespacedName{
			Namespace: "tenant-root",
			Name:      "bucket-cozy-backups-system",
		},
		CredentialsCOSISecretName: "bucket-cozy-backups-system",
		VeleroNamespace:           "cozy-velero",
		MinForceInterval:          5 * time.Minute,
		now:                       func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
	}
	g.Interface = dyn
	g.RESTMapper = gateRESTMapper()
	return g, dyn
}

func sourceSecret(bucket string) *corev1.Secret {
	data := map[string][]byte{
		"accessKey": []byte("AK"),
		"secretKey": []byte("SK"),
		"endpoint":  []byte("s3.example.com"),
	}
	if bucket != "" {
		data["bucketName"] = []byte(bucket)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-root", Name: "bucket-cozy-backups-system-credentials"},
		Data:       data,
	}
}

func bslObject() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("velero.io/v1")
	u.SetKind("BackupStorageLocation")
	u.SetNamespace("cozy-velero")
	u.SetName(defaultBackupStorageLocationName)
	u.SetAnnotations(map[string]string{endpointAnnotation: renderedEndpoint})
	return u
}

func forceAnnotations(t *testing.T, dyn *dynamicfake.FakeDynamicClient) map[string]string {
	t.Helper()
	return annotationsOf(t, dyn, "cozy-backup-controller", "backupstrategy-controller")
}

func credentialsForceAnnotations(t *testing.T, dyn *dynamicfake.FakeDynamicClient) map[string]string {
	t.Helper()
	return annotationsOf(t, dyn, "tenant-root", "bucket-cozy-backups-system")
}

func annotationsOf(t *testing.T, dyn *dynamicfake.FakeDynamicClient, namespace, name string) map[string]string {
	t.Helper()
	hr, err := dyn.Resource(helmReleaseGVR).Namespace(namespace).
		Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get HelmRelease %s/%s: %v", namespace, name, err)
	}
	return hr.GetAnnotations()
}

// TestCheckForcesWhenObjectsMissing is the bug this gate exists for: the
// bucket name resolves, so a Helm re-render WOULD produce the gated
// templates, but the objects are absent because the install-time lookup was
// empty and helm-controller never re-rendered. The gate must force a real
// Helm upgrade — BOTH annotations, because requestedAt alone is a no-op for
// an unchanged release.
func TestCheckForcesWhenObjectsMissing(t *testing.T) {
	g, dyn := newGate(t,
		[]client.Object{sourceSecret("bucket-1a2b"), cozyDefaultBackupClass()},
		helmReleaseObject(),
	)

	missing, forced, err := g.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !forced {
		t.Fatal("expected a forced Helm upgrade when default objects are missing")
	}
	// 6 strategyRefs (Velero twice under different names) + the BSL.
	if len(missing) != 7 {
		t.Fatalf("missing = %v, want 7 entries", missing)
	}
	ann := forceAnnotations(t, dyn)
	forceAt, ok := ann["reconcile.fluxcd.io/forceAt"]
	if !ok || forceAt == "" {
		t.Errorf("reconcile.fluxcd.io/forceAt not set: %v", ann)
	}
	requestedAt, ok := ann["reconcile.fluxcd.io/requestedAt"]
	if !ok || requestedAt == "" {
		t.Errorf("reconcile.fluxcd.io/requestedAt not set: %v", ann)
	}
	if forceAt != requestedAt {
		t.Errorf("forceAt (%q) and requestedAt (%q) must carry the same stamp", forceAt, requestedAt)
	}
}

// TestCheckNoopWhenAllPresent pins the steady state: no forced upgrades
// once the objects exist, otherwise the gate would rewrite the annotation
// forever and re-run a Helm upgrade every MinForceInterval for the life of
// the cluster.
func TestCheckNoopWhenAllPresent(t *testing.T) {
	g, dyn := newGate(t,
		[]client.Object{sourceSecret("bucket-1a2b"), cosiSecret("https://s3.example.com"), cozyDefaultBackupClass()},
		helmReleaseObject(),
		strategyObject("CNPG", "cozy-default-cnpg"),
		strategyObject("MariaDB", "cozy-default-mariadb"),
		strategyObject("Etcd", "cozy-default-etcd"),
		strategyObject("Altinity", "cozy-default-altinity"),
		strategyObject("Velero", "cozy-default-velero-vminstance"),
		strategyObject("Velero", "cozy-default-velero-vmdisk"),
		bslObject(),
	)

	missing, forced, err := g.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(missing) != 0 || forced {
		t.Fatalf("missing = %v, forced = %v, want none", missing, forced)
	}
	if ann := forceAnnotations(t, dyn); len(ann) != 0 {
		t.Errorf("HelmRelease annotated in the steady state: %v", ann)
	}
}

// TestCheckForcesCredentialsReleaseWhenSourceSecretAbsent is the second half
// of the bug. The Secret the gate reads to resolve the bucket name is itself
// rendered behind an install-time lookup, by the bucket's <bucket>-system
// release — so it is subject to the identical permanent skip, one release
// earlier. While it is absent nothing downstream can resolve: no projected
// credentials, no Strategy CRs, no Velero, and migration 50 has no snapshot
// target. Forcing THIS chart's release would be useless (its own lookups
// depend on that Secret); the bucket release is the one to force.
func TestCheckForcesCredentialsReleaseWhenSourceSecretAbsent(t *testing.T) {
	g, dyn := newGate(t,
		[]client.Object{cozyDefaultBackupClass()},
		helmReleaseObject(),
		credentialsHelmReleaseObject(),
	)

	missing, forced, err := g.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !forced {
		t.Fatal("expected the bucket release to be forced while the credentials Secret is absent")
	}
	if len(missing) != 1 || missing[0] != "Secret/bucket-cozy-backups-system-credentials" {
		t.Fatalf("missing = %v, want the credentials Secret", missing)
	}
	ann := credentialsForceAnnotations(t, dyn)
	if ann["reconcile.fluxcd.io/forceAt"] == "" || ann["reconcile.fluxcd.io/requestedAt"] == "" {
		t.Errorf("bucket release not forced with both annotations: %v", ann)
	}
	// This chart's own release must be left alone: its templates gate on the
	// same unresolved bucket, so forcing it would burn a Helm upgrade that
	// re-runs the same empty lookup.
	if ann := forceAnnotations(t, dyn); len(ann) != 0 {
		t.Errorf("own HelmRelease forced before the bucket resolved: %v", ann)
	}
}

// TestCheckForcesCredentialsReleaseWhenBucketNameEmpty pins the same
// treatment one state later: the Secret exists but carries no bucket name (a
// partial render, or a hand-written Secret missing the key). Only a
// re-render of the producing release can complete it.
func TestCheckForcesCredentialsReleaseWhenBucketNameEmpty(t *testing.T) {
	g, dyn := newGate(t,
		[]client.Object{sourceSecret(""), cozyDefaultBackupClass()},
		helmReleaseObject(),
		credentialsHelmReleaseObject(),
	)

	missing, forced, err := g.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !forced || len(missing) != 1 {
		t.Fatalf("missing = %v, forced = %v; want the credentials Secret forced", missing, forced)
	}
	if ann := credentialsForceAnnotations(t, dyn); ann["reconcile.fluxcd.io/forceAt"] == "" {
		t.Errorf("bucket release not forced: %v", ann)
	}
}

// TestCheckReportsCredentialsSecretWithoutBucketRelease covers external S3
// (backupStorage.provisionBucket=false): the Secret is admin-managed and no
// release renders it, so there is nothing to force — but it must still be
// reported missing, because that is the state an operator has to alert on.
func TestCheckReportsCredentialsSecretWithoutBucketRelease(t *testing.T) {
	g, dyn := newGate(t,
		[]client.Object{cozyDefaultBackupClass()},
		helmReleaseObject(),
		credentialsHelmReleaseObject(),
	)
	g.CredentialsHelmRelease = types.NamespacedName{}

	missing, forced, err := g.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if forced {
		t.Error("forced a release on the external-S3 path, where none renders the Secret")
	}
	if len(missing) != 1 {
		t.Fatalf("missing = %v, want the credentials Secret reported", missing)
	}
	if ann := credentialsForceAnnotations(t, dyn); len(ann) != 0 {
		t.Errorf("bucket release annotated with no coordinates configured: %v", ann)
	}
}

// TestCheckThrottlesTheTwoReleasesIndependently pins that forcing the bucket
// release does not eat the objects release's throttle budget. The two happen
// in sequence on a real bootstrap — credentials first, then the objects the
// resolved bucket unblocks — so a shared timestamp would delay the second
// force by a full MinForceInterval for no reason.
func TestCheckThrottlesTheTwoReleasesIndependently(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	g, dyn := newGate(t,
		[]client.Object{cozyDefaultBackupClass()},
		helmReleaseObject(),
		credentialsHelmReleaseObject(),
	)
	g.now = func() time.Time { return base }

	// No source Secret yet: the bucket release is forced.
	if _, forced, err := g.Check(context.Background()); err != nil || !forced {
		t.Fatalf("first Check: forced = %v, err = %v; want the bucket release forced", forced, err)
	}

	// A minute later the Secret lands. The objects are still missing, and
	// this force must go through despite being well inside MinForceInterval
	// of the previous one.
	g.now = func() time.Time { return base.Add(time.Minute) }
	if err := g.Client.Create(context.Background(), sourceSecret("bucket-1a2b")); err != nil {
		t.Fatalf("create source Secret: %v", err)
	}
	missing, forced, err := g.Check(context.Background())
	if err != nil {
		t.Fatalf("second Check: %v", err)
	}
	if !forced {
		t.Fatal("objects release throttled by the earlier credentials force")
	}
	if len(missing) != 7 {
		t.Fatalf("missing = %v, want the 6 strategies + the BSL", missing)
	}
	if ann := forceAnnotations(t, dyn); ann["reconcile.fluxcd.io/forceAt"] == "" {
		t.Errorf("own HelmRelease not forced: %v", ann)
	}
}

// TestForceSkipsSuspendedHelmRelease pins the suspend guard. helm-controller
// ignores forceAt/requestedAt while spec.suspend is true (`cozyhr suspend` is
// a standard dev workflow), so patching anyway would re-stamp every
// MinForceInterval for the whole suspension and climb
// cozystack_backup_default_objects_force_reconciles_total — which the runbook
// reads as "the forced render is not producing the objects", the wrong
// diagnosis. The objects must still be reported missing.
func TestForceSkipsSuspendedHelmRelease(t *testing.T) {
	g, dyn := newGate(t,
		[]client.Object{sourceSecret("bucket-1a2b"), cozyDefaultBackupClass()},
		suspended(helmReleaseObject()),
	)

	missing, forced, err := g.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if forced {
		t.Error("forced reported true for a suspended HelmRelease that ignores the annotations")
	}
	if len(missing) != 7 {
		t.Fatalf("missing = %v, want the objects still reported", missing)
	}
	if ann := forceAnnotations(t, dyn); len(ann) != 0 {
		t.Errorf("suspended HelmRelease annotated: %v", ann)
	}
}

// TestForceSkipsSuspendedCredentialsHelmRelease pins the same guard on the
// bucket release, which an operator is far more likely to have suspended:
// it lives in a tenant namespace and is not part of this chart.
func TestForceSkipsSuspendedCredentialsHelmRelease(t *testing.T) {
	g, dyn := newGate(t,
		[]client.Object{cozyDefaultBackupClass()},
		helmReleaseObject(),
		suspended(credentialsHelmReleaseObject()),
	)

	missing, forced, err := g.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if forced {
		t.Error("forced reported true for a suspended bucket HelmRelease")
	}
	if len(missing) != 1 {
		t.Fatalf("missing = %v, want the credentials Secret still reported", missing)
	}
	if ann := credentialsForceAnnotations(t, dyn); len(ann) != 0 {
		t.Errorf("suspended bucket HelmRelease annotated: %v", ann)
	}
}

// TestCheckTimeoutStaysBelowPeriod pins the bound on a single check. The ticker
// loop is sequential and the manager context is only cancelled at shutdown, so
// an unbounded check that hangs on a stalled API call stops every later check
// for the life of the pod.
func TestCheckTimeoutStaysBelowPeriod(t *testing.T) {
	for _, tc := range []struct {
		name   string
		period time.Duration
		want   time.Duration
	}{
		{"unset falls back to the cap", 0, 30 * time.Second},
		{"default period halves", time.Minute, 30 * time.Second},
		{"short period halves", 10 * time.Second, 5 * time.Second},
		{"long period is capped", time.Hour, 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &DefaultObjectsGate{Period: tc.period}
			got := g.checkTimeout()
			if got != tc.want {
				t.Fatalf("checkTimeout() = %v, want %v", got, tc.want)
			}
			if tc.period > 0 && got >= tc.period {
				t.Fatalf("checkTimeout() = %v, must stay below Period %v", got, tc.period)
			}
		})
	}
}

// TestCheckThrottlesRepeatedForces pins the throttle. A condition a
// re-render cannot fix (missing CRD, unreachable bucket) must not turn into
// a hot loop of Helm upgrades against helm-controller.
func TestCheckThrottlesRepeatedForces(t *testing.T) {
	g, _ := newGate(t,
		[]client.Object{sourceSecret("bucket-1a2b"), cozyDefaultBackupClass()},
		helmReleaseObject(),
	)
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	g.now = func() time.Time { return base }

	if _, forced, err := g.Check(context.Background()); err != nil || !forced {
		t.Fatalf("first Check: forced = %v, err = %v; want forced", forced, err)
	}

	// Well inside MinForceInterval: still missing, but no second upgrade.
	g.now = func() time.Time { return base.Add(time.Minute) }
	missing, forced, err := g.Check(context.Background())
	if err != nil {
		t.Fatalf("second Check: %v", err)
	}
	if forced {
		t.Error("second Check forced inside MinForceInterval")
	}
	if len(missing) == 0 {
		t.Error("second Check should still report the objects as missing")
	}

	// Past MinForceInterval: force again, because the objects are still gone.
	g.now = func() time.Time { return base.Add(6 * time.Minute) }
	if _, forced, err := g.Check(context.Background()); err != nil || !forced {
		t.Fatalf("third Check: forced = %v, err = %v; want forced", forced, err)
	}
}

// TestMissingObjectsIgnoresUnmappedKinds pins that a strategyRef whose CRD
// is not installed is not counted as missing. No Helm re-render can create
// such an object, so counting it would force upgrades forever.
func TestMissingObjectsIgnoresUnmappedKinds(t *testing.T) {
	bc := cozyDefaultBackupClass()
	bc.Spec.Strategies = append(bc.Spec.Strategies, backupsv1alpha1.BackupClassStrategy{
		Application: backupsv1alpha1.ApplicationSelector{Kind: "Nonexistent"},
		StrategyRef: corev1.TypedLocalObjectReference{
			APIGroup: new(strategyAPIGroup),
			Kind:     "NotInstalled",
			Name:     "cozy-default-notinstalled",
		},
	})
	g, _ := newGate(t, []client.Object{sourceSecret("b"), bc},
		helmReleaseObject(),
		strategyObject("CNPG", "cozy-default-cnpg"),
		strategyObject("MariaDB", "cozy-default-mariadb"),
		strategyObject("Etcd", "cozy-default-etcd"),
		strategyObject("Altinity", "cozy-default-altinity"),
		strategyObject("Velero", "cozy-default-velero-vminstance"),
		strategyObject("Velero", "cozy-default-velero-vmdisk"),
		bslObject(),
	)

	state, err := g.inspectObjects(context.Background(), bc, "")
	if err != nil {
		t.Fatalf("inspectObjects: %v", err)
	}
	missing := state.missing
	if len(missing) != 0 {
		t.Fatalf("missing = %v, want none (unmapped kind must be ignored)", missing)
	}
}

// TestMissingObjectsSkipsVeleroWhenNamespaceEmpty covers
// velero.bslEnabled=false: the chart gates BOTH the BSL and the Velero
// Strategy CRs off the same flag, so neither is rendered and their absence
// is not a defect. The BackupClass still routes to the Velero strategies
// unconditionally, which is the trap: without the skip the gate would count
// the never-rendered Velero CRs as missing and force a Helm upgrade every
// MinForceInterval forever. The Velero objects are deliberately NOT
// pre-created here, because with the BSL disabled the chart never renders
// them on a real cluster.
func TestMissingObjectsSkipsVeleroWhenNamespaceEmpty(t *testing.T) {
	bc := cozyDefaultBackupClass()
	g, _ := newGate(t, []client.Object{sourceSecret("b"), bc},
		helmReleaseObject(),
		strategyObject("CNPG", "cozy-default-cnpg"),
		strategyObject("MariaDB", "cozy-default-mariadb"),
		strategyObject("Etcd", "cozy-default-etcd"),
		strategyObject("Altinity", "cozy-default-altinity"),
	)
	g.VeleroNamespace = ""

	state, err := g.inspectObjects(context.Background(), bc, "")
	if err != nil {
		t.Fatalf("inspectObjects: %v", err)
	}
	missing := state.missing
	if len(missing) != 0 {
		t.Fatalf("missing = %v, want none when Velero is disabled", missing)
	}
}

// TestMissingObjectsSkipsBSLWhenVeleroAPIAbsent covers Velero being removed
// after bootstrap while bslEnabled=true: the velero.io API is no longer
// served, so the BSL cannot be re-rendered and must not be counted as
// missing. Resolving through the RESTMapper turns that into a NoMatch skip;
// the previous hardcoded-GVR Get returned a NotFound that would have looped
// the gate forever.
func TestMissingObjectsSkipsBSLWhenVeleroAPIAbsent(t *testing.T) {
	bc := cozyDefaultBackupClass()
	g, _ := newGate(t, []client.Object{sourceSecret("b"), bc},
		helmReleaseObject(),
		strategyObject("CNPG", "cozy-default-cnpg"),
		strategyObject("MariaDB", "cozy-default-mariadb"),
		strategyObject("Etcd", "cozy-default-etcd"),
		strategyObject("Altinity", "cozy-default-altinity"),
		strategyObject("Velero", "cozy-default-velero-vminstance"),
		strategyObject("Velero", "cozy-default-velero-vmdisk"),
	)
	// Rebuild the RESTMapper without BackupStorageLocation: the velero.io API
	// is not served on this cluster.
	gvset := map[schema.GroupVersion]struct{}{}
	var kept []schema.GroupVersionKind
	for _, gvk := range gateGVKs {
		if gvk.Kind == "BackupStorageLocation" {
			continue
		}
		gvset[gvk.GroupVersion()] = struct{}{}
		kept = append(kept, gvk)
	}
	var gvs []schema.GroupVersion
	for gv := range gvset {
		gvs = append(gvs, gv)
	}
	m := meta.NewDefaultRESTMapper(gvs)
	for _, gvk := range kept {
		scope := meta.RESTScopeRoot
		if gvk.Kind == "HelmRelease" {
			scope = meta.RESTScopeNamespace
		}
		m.Add(gvk, scope)
	}
	g.RESTMapper = m

	state, err := g.inspectObjects(context.Background(), bc, "")
	if err != nil {
		t.Fatalf("inspectObjects: %v", err)
	}
	missing := state.missing
	if len(missing) != 0 {
		t.Fatalf("missing = %v, want none when the Velero API is absent", missing)
	}
}

// TestCheckSurfacesPatchFailure pins that a failed force is reported rather
// than silently recorded as done — and that lastForce is NOT advanced, so
// the next tick retries instead of waiting out MinForceInterval.
func TestCheckSurfacesPatchFailure(t *testing.T) {
	g, dyn := newGate(t,
		[]client.Object{sourceSecret("bucket-1a2b"), cozyDefaultBackupClass()},
		helmReleaseObject(),
	)
	dyn.PrependReactor("patch", "helmreleases", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"}, "backupstrategy-controller", nil)
	})

	if _, forced, err := g.Check(context.Background()); err == nil || forced {
		t.Fatalf("forced = %v, err = %v; want an error and forced=false", forced, err)
	}
	if !g.lastForce.IsZero() {
		t.Error("lastForce advanced despite the patch failing; the next tick would be throttled")
	}
}

// TestMissingGaugeCoversTheUnresolvedPath pins the alerting contract. The
// gauge used to be written only after the bucket resolved, so the state the
// gate exists to catch — no credentials Secret, therefore no strategies —
// reported 0 or nothing at all, and an alert on it never fired. Deleting the
// source Secret later (credential rotation) had the same effect: the gauge
// froze at its last value.
func TestMissingGaugeCoversTheUnresolvedPath(t *testing.T) {
	defaultObjectsMissing.Reset()
	g, _ := newGate(t,
		[]client.Object{cozyDefaultBackupClass()},
		helmReleaseObject(),
		credentialsHelmReleaseObject(),
	)

	if _, _, err := g.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got := testutil.ToFloat64(defaultObjectsMissing.WithLabelValues("cozy-default")); got != 1 {
		t.Fatalf("cozystack_backup_default_objects_missing = %v, want 1 while the credentials Secret is absent", got)
	}
}

// TestCheckErrorsCounterMarksTheGaugeStale pins the other half of that
// contract. On an API error the gauge deliberately keeps its last value
// rather than flapping the alert, which means the gauge alone cannot tell
// "healthy" from "not evaluated". The errors counter is what closes that
// gap, and the runbook alerts on both.
func TestCheckErrorsCounterMarksTheGaugeStale(t *testing.T) {
	defaultObjectsMissing.Reset()
	defaultObjectsCheckErrors.Reset()
	// No BackupClass: the check cannot enumerate what must exist.
	g, _ := newGate(t,
		[]client.Object{sourceSecret("bucket-1a2b")},
		helmReleaseObject(),
	)

	if _, _, err := g.Check(context.Background()); err == nil {
		t.Fatal("Check succeeded with no BackupClass, want an error")
	}
	if got := testutil.ToFloat64(defaultObjectsCheckErrors.WithLabelValues("cozy-default")); got != 1 {
		t.Fatalf("cozystack_backup_default_objects_check_errors_total = %v, want 1", got)
	}
	if got := testutil.ToFloat64(defaultObjectsMissing.WithLabelValues("cozy-default")); got != 0 {
		t.Fatalf("gauge = %v, want it left untouched by a failed check", got)
	}
}

// TestStartDisabledWithoutHelmRelease pins the opt-out: without release
// coordinates the runnable must return immediately instead of ticking with
// a nil target.
func TestStartDisabledWithoutHelmRelease(t *testing.T) {
	g, _ := newGate(t, []client.Object{sourceSecret("b"), cozyDefaultBackupClass()}, helmReleaseObject())
	g.HelmRelease = types.NamespacedName{}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- g.Start(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("Start did not return immediately when disabled")
	}
}

// TestCheckToleratesAbsentHelmRelease covers a plain `helm install` (local
// development): there is no HelmRelease to force, which is not an error to
// report every tick.
func TestCheckToleratesAbsentHelmRelease(t *testing.T) {
	g, _ := newGate(t, []client.Object{sourceSecret("bucket-1a2b"), cozyDefaultBackupClass()})

	missing, forced, err := g.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if forced {
		t.Error("forced reported true with no HelmRelease to patch")
	}
	if len(missing) == 0 {
		t.Error("objects should still be reported as missing")
	}
}

// TestCheckForcesCredentialsReleaseWhenEndpointStale covers the credentials
// Secret outliving the endpoint it was rendered from. The COSI sidecar writes
// BucketInfo once per BucketAccess, so a re-granted BucketAccess (after a
// publishing.host change) carries the new endpoint while the credentials
// Secret <bucket>-system rendered earlier keeps the old one: the Secret
// exists, so the missing-Secret path never fires, and every consumer keeps
// the dead host.
func TestCheckForcesCredentialsReleaseWhenEndpointStale(t *testing.T) {
	objs := append([]runtime.Object{helmReleaseObject(), credentialsHelmReleaseObject()}, allDefaultObjects()...)
	g, dyn := newGate(t,
		[]client.Object{sourceSecret("bucket-1a2b"), cosiSecret("https://s3.new.example.com"), cozyDefaultBackupClass()},
		objs...,
	)

	missing, forced, err := g.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !forced {
		t.Fatal("expected the bucket release forced while its credentials Secret carries a stale endpoint")
	}
	if len(missing) != 1 || !strings.HasPrefix(missing[0], "Secret/bucket-cozy-backups-system-credentials") {
		t.Fatalf("missing = %v, want the credentials Secret", missing)
	}
	if ann := credentialsForceAnnotations(t, dyn); ann["reconcile.fluxcd.io/forceAt"] == "" {
		t.Errorf("bucket release not forced: %v", ann)
	}
	// The objects were rendered from the stale Secret and agree with it;
	// re-rendering them before the Secret is fixed would bake the old
	// endpoint in again.
	if ann := forceAnnotations(t, dyn); len(ann) != 0 {
		t.Errorf("own HelmRelease forced while the credentials Secret is stale: %v", ann)
	}
}

// TestCheckCredentialsEndpointComparison pins the comparison against what
// user-credentials.yaml writes for a given BucketInfo endpoint (the scheme
// stripped, nothing else), so a Secret the chart rendered from the current
// BucketInfo never counts as stale and the bucket release is not forced on
// every throttle window.
func TestCheckCredentialsEndpointComparison(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cosi      string
		wantForce bool
	}{
		{"https scheme stripped", "https://s3.example.com", false},
		{"http scheme stripped", "http://s3.example.com", false},
		{"bare host", "s3.example.com", false},
		{"other host", "https://s3.new.example.com", true},
		{"other port", "https://s3.example.com:8443", true},
		// A forced render from a BucketInfo with no endpoint would write an
		// empty endpoint into the Secret every driver reads (or, with the key
		// absent, fail the bucket release's render), never a working one.
		{"no endpoint in BucketInfo", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objs := append([]runtime.Object{helmReleaseObject(), credentialsHelmReleaseObject()}, allDefaultObjects()...)
			g, dyn := newGate(t,
				[]client.Object{sourceSecret("bucket-1a2b"), cosiSecret(tc.cosi), cozyDefaultBackupClass()},
				objs...,
			)
			if _, _, err := g.Check(context.Background()); err != nil {
				t.Fatalf("Check: %v", err)
			}
			forced := credentialsForceAnnotations(t, dyn)["reconcile.fluxcd.io/forceAt"] != ""
			if forced != tc.wantForce {
				t.Fatalf("bucket release forced = %v, want %v", forced, tc.wantForce)
			}
		})
	}
}

// TestCheckSkipsCredentialsComparisonWithoutCOSISecret covers the window in
// which the BucketAccess is being re-granted: the COSI Secret is gone and
// there is nothing to compare against, which is not evidence of staleness.
func TestCheckSkipsCredentialsComparisonWithoutCOSISecret(t *testing.T) {
	objs := append([]runtime.Object{helmReleaseObject(), credentialsHelmReleaseObject()}, allDefaultObjects()...)
	g, dyn := newGate(t,
		[]client.Object{sourceSecret("bucket-1a2b"), cozyDefaultBackupClass()},
		objs...,
	)

	missing, forced, err := g.Check(context.Background())
	if err != nil || forced || len(missing) != 0 {
		t.Fatalf("missing = %v, forced = %v, err = %v; want a no-op", missing, forced, err)
	}
	if ann := credentialsForceAnnotations(t, dyn); len(ann) != 0 {
		t.Errorf("bucket release forced with no COSI Secret to compare against: %v", ann)
	}
}

// TestCheckSkipsCredentialsComparisonWhenUnconfigured pins both switches that
// turn the comparison off: no COSI Secret name, and no bucket release (external
// S3, where the Secret is admin-managed and no BucketInfo backs it).
func TestCheckSkipsCredentialsComparisonWhenUnconfigured(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(g *DefaultObjectsGate)
	}{
		{"no COSI Secret name", func(g *DefaultObjectsGate) { g.CredentialsCOSISecretName = "" }},
		{"no bucket release", func(g *DefaultObjectsGate) { g.CredentialsHelmRelease = types.NamespacedName{} }},
		{"no bucket release name", func(g *DefaultObjectsGate) { g.CredentialsHelmRelease.Name = "" }},
		{"no bucket release namespace", func(g *DefaultObjectsGate) { g.CredentialsHelmRelease.Namespace = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objs := append([]runtime.Object{helmReleaseObject(), credentialsHelmReleaseObject()}, allDefaultObjects()...)
			g, dyn := newGate(t,
				[]client.Object{sourceSecret("bucket-1a2b"), cosiSecret("https://s3.new.example.com"), cozyDefaultBackupClass()},
				objs...,
			)
			tc.apply(g)
			// The real API server rejects a Get with an empty name; the fake
			// answers NotFound, which would hide a lookup of an unset name.
			g.Client = interceptor.NewClient(g.Client.(client.WithWatch), interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if key.Name == "" {
						return errors.New("resource name may not be empty")
					}
					return c.Get(ctx, key, obj, opts...)
				},
			})
			missing, forced, err := g.Check(context.Background())
			if err != nil || forced || len(missing) != 0 {
				t.Fatalf("missing = %v, forced = %v, err = %v; want a no-op", missing, forced, err)
			}
			if ann := credentialsForceAnnotations(t, dyn); len(ann) != 0 {
				t.Errorf("bucket release forced with the comparison disabled: %v", ann)
			}
		})
	}
}

// TestCheckReportsMalformedCOSISecret pins that a COSI Secret the gate cannot
// read is an error the check counts, not a silent "not stale".
func TestCheckReportsMalformedCOSISecret(t *testing.T) {
	defaultObjectsCheckErrors.Reset()
	bad := cosiSecret("")
	bad.Data["BucketInfo"] = []byte("{not json")
	objs := append([]runtime.Object{helmReleaseObject(), credentialsHelmReleaseObject()}, allDefaultObjects()...)
	g, _ := newGate(t, []client.Object{sourceSecret("bucket-1a2b"), bad, cozyDefaultBackupClass()}, objs...)

	if _, forced, err := g.Check(context.Background()); err == nil || forced {
		t.Fatalf("forced = %v, err = %v; want an error", forced, err)
	}
	if got := testutil.ToFloat64(defaultObjectsCheckErrors.WithLabelValues("cozy-default")); got != 1 {
		t.Fatalf("check errors = %v, want 1", got)
	}
}

// TestCheckForcesOwnReleaseWhenObjectEndpointStale is the second half of a
// publishing.host change: the credentials Secret now carries the new
// endpoint, but the BSL and the default Strategies baked the old one at the
// last render of this chart and nothing re-renders them, so every backup
// keeps going to the dead host.
func TestCheckForcesOwnReleaseWhenObjectEndpointStale(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stale *unstructured.Unstructured
		key   string
	}{
		{"strategy", strategyObject("CNPG", "cozy-default-cnpg"), "CNPG/cozy-default-cnpg"},
		{"BSL", bslObject(), "BackupStorageLocation/cozy-default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var objs []runtime.Object
			for _, o := range allDefaultObjects() {
				u := o.(*unstructured.Unstructured)
				if u.GetKind() == tc.stale.GetKind() && u.GetName() == tc.stale.GetName() {
					withEndpoint(u, "https://s3.old.example.com")
				}
				objs = append(objs, u)
			}
			objs = append(objs, helmReleaseObject(), credentialsHelmReleaseObject())
			g, dyn := newGate(t,
				[]client.Object{sourceSecret("bucket-1a2b"), cosiSecret("https://s3.example.com"), cozyDefaultBackupClass()},
				objs...,
			)

			missing, forced, err := g.Check(context.Background())
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if !forced {
				t.Fatal("expected this chart's release forced while an object carries a stale endpoint")
			}
			if len(missing) != 1 || !strings.HasPrefix(missing[0], tc.key+" ") {
				t.Fatalf("missing = %v, want only %s", missing, tc.key)
			}
			if ann := forceAnnotations(t, dyn); ann["reconcile.fluxcd.io/forceAt"] == "" {
				t.Errorf("own HelmRelease not forced: %v", ann)
			}
			if ann := credentialsForceAnnotations(t, dyn); len(ann) != 0 {
				t.Errorf("bucket release forced for a stale object: %v", ann)
			}
		})
	}
}

// TestCheckIgnoresObjectEndpointOnExternalS3 pins that the comparison is off
// when no bucket release is configured: with provisionBucket=false the chart
// renders backupStorage.endpoint, not the Secret's, so the two legitimately
// differ and forcing would loop every MinForceInterval.
func TestCheckIgnoresObjectEndpointOnExternalS3(t *testing.T) {
	objs := []runtime.Object{helmReleaseObject()}
	for _, o := range allDefaultObjects() {
		objs = append(objs, withEndpoint(o.(*unstructured.Unstructured), "https://minio.example.internal:9000"))
	}
	g, dyn := newGate(t, []client.Object{sourceSecret("bucket-1a2b"), cozyDefaultBackupClass()}, objs...)
	g.CredentialsHelmRelease = types.NamespacedName{}

	missing, forced, err := g.Check(context.Background())
	if err != nil || forced || len(missing) != 0 {
		t.Fatalf("missing = %v, forced = %v, err = %v; want a no-op", missing, forced, err)
	}
	if ann := forceAnnotations(t, dyn); len(ann) != 0 {
		t.Errorf("own HelmRelease forced on external S3: %v", ann)
	}
}

// TestCheckIgnoresObjectEndpointWithoutSecretEndpoint covers a credentials
// Secret with no endpoint key: the chart then renders its values fallback,
// which the gate cannot reproduce, so there is nothing to compare against.
func TestCheckIgnoresObjectEndpointWithoutSecretEndpoint(t *testing.T) {
	src := sourceSecret("bucket-1a2b")
	delete(src.Data, "endpoint")
	objs := append([]runtime.Object{helmReleaseObject(), credentialsHelmReleaseObject()}, allDefaultObjects()...)
	g, dyn := newGate(t, []client.Object{src, cozyDefaultBackupClass()}, objs...)

	if _, forced, err := g.Check(context.Background()); err != nil || forced {
		t.Fatalf("forced = %v, err = %v; want a no-op", forced, err)
	}
	if ann := forceAnnotations(t, dyn); len(ann) != 0 {
		t.Errorf("own HelmRelease forced with no endpoint to compare against: %v", ann)
	}
}

// TestCheckKeepsForcingForUnannotatedObjects covers objects rendered by a
// chart that predates the endpoint annotation. Their endpoint is unknown, so
// they count as stale until a render stamps the annotation. Having requested
// a render proves nothing: one that failed leaves them unannotated, and the
// next unthrottled check must force again rather than fall silent, so the
// force counter keeps reporting the render that does not land.
func TestCheckKeepsForcingForUnannotatedObjects(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	objs := []runtime.Object{helmReleaseObject(), credentialsHelmReleaseObject()}
	for _, o := range allDefaultObjects() {
		objs = append(objs, unannotated(o.(*unstructured.Unstructured)))
	}
	g, dyn := newGate(t, []client.Object{sourceSecret("bucket-1a2b"), cozyDefaultBackupClass()}, objs...)
	g.lastForce = base.Add(-time.Minute)
	g.now = func() time.Time { return base }

	if missing, forced, err := g.Check(context.Background()); err != nil || forced || len(missing) != 7 {
		t.Fatalf("throttled Check: missing = %v, forced = %v, err = %v; want 7 reported, none forced", missing, forced, err)
	}

	g.now = func() time.Time { return base.Add(5 * time.Minute) }
	if _, forced, err := g.Check(context.Background()); err != nil || !forced {
		t.Fatalf("first unthrottled Check: forced = %v, err = %v; want forced", forced, err)
	}
	first := forceAnnotations(t, dyn)["reconcile.fluxcd.io/forceAt"]

	g.now = func() time.Time { return base.Add(time.Hour) }
	missing, forced, err := g.Check(context.Background())
	if err != nil || !forced || len(missing) != 7 {
		t.Fatalf("after a render that left them unannotated: missing = %v, forced = %v, err = %v; want 7 reported and forced again", missing, forced, err)
	}
	if again := forceAnnotations(t, dyn)["reconcile.fluxcd.io/forceAt"]; again == first {
		t.Errorf("not forced again for unannotated objects: forceAt still %q", first)
	}
}

// TestRenderedEndpointMatchesTheChart pins renderedEndpointFor to the
// "backupstrategy-controller.endpoint" helper, which the chart stamps as the
// annotation: trim, strip a scheme, force https://. Any divergence makes the
// gate see every object as stale and force this release forever.
func TestRenderedEndpointMatchesTheChart(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"s3.example.com", "https://s3.example.com"},
		{"s3.example.com\n", "https://s3.example.com"},
		{" s3.example.com ", "https://s3.example.com"},
		{"https://s3.example.com", "https://s3.example.com"},
		{"http://s3.example.com:8333", "https://s3.example.com:8333"},
		{"s3.example.com/prefix/", "https://s3.example.com/prefix/"},
		// No endpoint in the Secret: the chart renders its values fallback,
		// which the gate cannot reproduce, so it compares nothing.
		{"", ""},
		// Present but blank: the helper's lookup sees a non-empty value and
		// renders the bare scheme.
		{"\n", "https://"},
	} {
		if got := renderedEndpointFor([]byte(tc.in)); got != tc.want {
			t.Errorf("renderedEndpointFor(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestForcedReleasesForNamesEachRenderingRelease pins the releases the log
// line names: the credentials Secret, missing or stale, is repaired by the
// bucket release, everything else by this chart's own, and both are named when
// one check forced both.
func TestForcedReleasesForNamesEachRenderingRelease(t *testing.T) {
	g, _ := newGate(t, nil)
	creds, own := g.CredentialsHelmRelease.String(), g.HelmRelease.String()
	for _, tc := range []struct {
		missing []string
		want    []string
	}{
		{[]string{"Secret/bucket-cozy-backups-system-credentials"}, []string{creds}},
		{[]string{`Secret/bucket-cozy-backups-system-credentials (endpoint "s3.old", COSI Secret has "s3.new")`}, []string{creds}},
		{[]string{"Secret/bucket-cozy-backups-system-credentials-other"}, []string{own}},
		{[]string{`CNPG/cozy-default-cnpg (endpoint "https://s3.old", want "https://s3.new")`, "Etcd/cozy-default-etcd"}, []string{own}},
		{[]string{`Secret/bucket-cozy-backups-system-credentials (endpoint "s3.old", COSI Secret has "s3.new")`, "CNPG/cozy-default-cnpg"}, []string{creds, own}},
	} {
		if got := g.forcedReleasesFor(tc.missing); !slices.Equal(got, tc.want) {
			t.Errorf("forcedReleasesFor(%v) = %v, want %v", tc.missing, got, tc.want)
		}
	}
	// External S3: no bucket release, so nothing but this chart's own.
	g.CredentialsHelmRelease = types.NamespacedName{}
	if got := g.forcedReleasesFor([]string{"Secret/bucket-cozy-backups-system-credentials"}); !slices.Equal(got, []string{own}) {
		t.Errorf("forcedReleasesFor without a bucket release = %v, want [%s]", got, own)
	}
}

// TestCheckSkipsCredentialsComparisonWithoutDerivedSecret covers a
// systemSecretName the bucket chart does not render: the same name as the COSI
// Secret (the projector also reads a raw COSI Secret) or a Secret without the
// endpoint key. Comparing it would report it stale on every tick, or fail the
// check, and the missing objects would never be repaired.
func TestCheckSkipsCredentialsComparisonWithoutDerivedSecret(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(g *DefaultObjectsGate, src *corev1.Secret)
	}{
		{"COSI Secret is the source", func(g *DefaultObjectsGate, src *corev1.Secret) {
			g.CredentialsCOSISecretName = g.Config.SourceSecretName
			src.Data["BucketInfo"] = cosiSecret("https://s3.new.example.com").Data["BucketInfo"]
		}},
		{"source without endpoint", func(_ *DefaultObjectsGate, src *corev1.Secret) {
			delete(src.Data, "endpoint")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := sourceSecret("bucket-1a2b")
			var objs []runtime.Object
			for _, o := range allDefaultObjects() {
				if u := o.(*unstructured.Unstructured); u.GetName() != "cozy-default-cnpg" {
					objs = append(objs, u)
				}
			}
			objs = append(objs, helmReleaseObject(), credentialsHelmReleaseObject())
			g, dyn := newGate(t, nil, objs...)
			tc.apply(g, src)
			g.Client = fake.NewClientBuilder().WithScheme(gateScheme()).
				WithObjects(src, cosiSecret("https://s3.new.example.com"), cozyDefaultBackupClass()).Build()

			missing, forced, err := g.Check(context.Background())
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if !forced || len(missing) != 1 || missing[0] != "CNPG/cozy-default-cnpg" {
				t.Fatalf("missing = %v, forced = %v; want the missing strategy repaired", missing, forced)
			}
			if ann := credentialsForceAnnotations(t, dyn); len(ann) != 0 {
				t.Errorf("bucket release forced: %v", ann)
			}
		})
	}
}

// TestCheckRepairsMissingObjectsWhileCredentialsUnresolved pins that a stale
// or unreadable credentials comparison does not cost the repair of missing
// objects the gate made before it existed. The comparison only withholds the
// endpoint check, whose answer would be rendered from the same stale Secret.
func TestCheckRepairsMissingObjectsWhileCredentialsUnresolved(t *testing.T) {
	malformed := cosiSecret("")
	malformed.Data["BucketInfo"] = []byte("{not json")
	for _, tc := range []struct {
		name          string
		cosi          *corev1.Secret
		credsHR       *unstructured.Unstructured
		wantErr       bool
		wantCredsHRAt bool
	}{
		{"stale credentials", cosiSecret("https://s3.new.example.com"), credentialsHelmReleaseObject(), false, true},
		{"stale credentials, bucket release suspended", cosiSecret("https://s3.new.example.com"), suspended(credentialsHelmReleaseObject()), false, false},
		{"malformed COSI Secret", malformed, credentialsHelmReleaseObject(), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defaultObjectsCheckErrors.Reset()
			defaultObjectsMissing.Reset()
			var objs []runtime.Object
			for _, o := range allDefaultObjects() {
				if u := o.(*unstructured.Unstructured); u.GetName() != "cozy-default-cnpg" {
					objs = append(objs, u)
				}
			}
			objs = append(objs, helmReleaseObject(), tc.credsHR)
			g, dyn := newGate(t, []client.Object{sourceSecret("bucket-1a2b"), tc.cosi, cozyDefaultBackupClass()}, objs...)

			missing, forced, err := g.Check(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if !forced {
				t.Error("forced = false, want this chart's release forced for the missing strategy")
			}
			if ann := forceAnnotations(t, dyn); ann["reconcile.fluxcd.io/forceAt"] == "" {
				t.Errorf("own HelmRelease not forced for a missing strategy: %v", ann)
			}
			if got := credentialsForceAnnotations(t, dyn)["reconcile.fluxcd.io/forceAt"] != ""; got != tc.wantCredsHRAt {
				t.Errorf("bucket release forced = %v, want %v", got, tc.wantCredsHRAt)
			}
			found := false
			for _, m := range missing {
				found = found || m == "CNPG/cozy-default-cnpg"
			}
			if !found {
				t.Errorf("missing = %v, want CNPG/cozy-default-cnpg reported", missing)
			}
			if got := testutil.ToFloat64(defaultObjectsMissing.WithLabelValues("cozy-default")); got != 1 {
				t.Errorf("missing gauge = %v, want 1", got)
			}
			wantErrs := 0.0
			if tc.wantErr {
				wantErrs = 1
			}
			if got := testutil.ToFloat64(defaultObjectsCheckErrors.WithLabelValues("cozy-default")); got != wantErrs {
				t.Errorf("check errors = %v, want %v", got, wantErrs)
			}
		})
	}
}

// TestCheckCountsOneErrorPerCheck pins that a check failing in two places is
// still one check that did not reach a conclusion.
func TestCheckCountsOneErrorPerCheck(t *testing.T) {
	defaultObjectsCheckErrors.Reset()
	bad := cosiSecret("")
	bad.Data["BucketInfo"] = []byte("{not json")
	g, _ := newGate(t, []client.Object{sourceSecret("bucket-1a2b"), bad}, helmReleaseObject(), credentialsHelmReleaseObject())

	if _, _, err := g.Check(context.Background()); err == nil {
		t.Fatal("Check succeeded with a malformed COSI Secret and no BackupClass")
	}
	if got := testutil.ToFloat64(defaultObjectsCheckErrors.WithLabelValues("cozy-default")); got != 1 {
		t.Fatalf("check errors = %v, want 1", got)
	}
}

// TestCheckSurfacesCredentialsPatchFailure pins that a failed force of the
// bucket release is reported, not recorded as done.
func TestCheckSurfacesCredentialsPatchFailure(t *testing.T) {
	objs := append([]runtime.Object{helmReleaseObject(), credentialsHelmReleaseObject()}, allDefaultObjects()...)
	g, dyn := newGate(t,
		[]client.Object{sourceSecret("bucket-1a2b"), cosiSecret("https://s3.new.example.com"), cozyDefaultBackupClass()},
		objs...,
	)
	dyn.PrependReactor("patch", "helmreleases", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"}, "bucket-cozy-backups-system", nil)
	})

	if _, forced, err := g.Check(context.Background()); err == nil || forced {
		t.Fatalf("forced = %v, err = %v; want an error and forced=false", forced, err)
	}
	if !g.lastCredentialsForce.IsZero() {
		t.Error("lastCredentialsForce advanced despite the patch failing")
	}
}

// TestCheckForcesStaleCredentialsWhenObjectsUnreadable pins that the
// credentials comparison needs only the two Secrets: a BackupClass or a routed
// object the gate cannot read must not keep the stale endpoint in the
// credentials Secret, which every other driver reads through the projector.
func TestCheckForcesStaleCredentialsWhenObjectsUnreadable(t *testing.T) {
	for _, tc := range []struct {
		name        string
		backupClass bool
		forbidCNPG  bool
	}{
		{"BackupClass absent", false, false},
		{"routed Strategy unreadable", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defaultObjectsCheckErrors.Reset()
			ctrlObjs := []client.Object{sourceSecret("bucket-1a2b"), cosiSecret("https://s3.new.example.com")}
			if tc.backupClass {
				ctrlObjs = append(ctrlObjs, cozyDefaultBackupClass())
			}
			objs := append([]runtime.Object{helmReleaseObject(), credentialsHelmReleaseObject()}, allDefaultObjects()...)
			g, dyn := newGate(t, ctrlObjs, objs...)
			if tc.forbidCNPG {
				dyn.PrependReactor("get", "cnpgs", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: strategyAPIGroup, Resource: "cnpgs"}, "cozy-default-cnpg", nil)
				})
			}

			missing, forced, err := g.Check(context.Background())
			if err == nil {
				t.Fatal("Check succeeded although the routed objects could not be evaluated")
			}
			if !forced {
				t.Error("forced = false, want the bucket release forced for its stale credentials Secret")
			}
			if ann := credentialsForceAnnotations(t, dyn); ann["reconcile.fluxcd.io/forceAt"] == "" {
				t.Errorf("bucket release not forced: %v", ann)
			}
			if ann := forceAnnotations(t, dyn); len(ann) != 0 {
				t.Errorf("own HelmRelease forced without a conclusive object check: %v", ann)
			}
			if len(missing) != 1 || !strings.HasPrefix(missing[0], "Secret/bucket-cozy-backups-system-credentials") {
				t.Errorf("missing = %v, want the credentials Secret", missing)
			}
			if got := testutil.ToFloat64(defaultObjectsCheckErrors.WithLabelValues("cozy-default")); got != 1 {
				t.Errorf("check errors = %v, want 1", got)
			}
		})
	}
}
