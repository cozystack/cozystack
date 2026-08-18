package backupcontroller

import (
	"context"
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

func apiGroup(s string) *string { return &s }

// cozyDefaultBackupClass mirrors the routes the chart's
// backupclass-default.yaml renders unconditionally: it is the manifest of
// what must exist, which is exactly why the gate reads it instead of
// hard-coding a list of Strategy names.
func cozyDefaultBackupClass() *backupsv1alpha1.BackupClass {
	ref := func(kind, name string) backupsv1alpha1.BackupClassStrategy {
		return backupsv1alpha1.BackupClassStrategy{
			Application: backupsv1alpha1.ApplicationSelector{
				APIGroup: apiGroup("apps.cozystack.io"),
				Kind:     kind + "App",
			},
			StrategyRef: corev1.TypedLocalObjectReference{
				APIGroup: apiGroup(strategyAPIGroup),
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

func strategyObject(kind, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(strategyAPIGroup + "/v1alpha1")
	u.SetKind(kind)
	u.SetName(name)
	return u
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
		VeleroNamespace:  "cozy-velero",
		MinForceInterval: 5 * time.Minute,
		now:              func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
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
		[]client.Object{sourceSecret("bucket-1a2b"), cozyDefaultBackupClass()},
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
			APIGroup: apiGroup(strategyAPIGroup),
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

	missing, err := g.missingObjects(context.Background(), bc)
	if err != nil {
		t.Fatalf("missingObjects: %v", err)
	}
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

	missing, err := g.missingObjects(context.Background(), bc)
	if err != nil {
		t.Fatalf("missingObjects: %v", err)
	}
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

	missing, err := g.missingObjects(context.Background(), bc)
	if err != nil {
		t.Fatalf("missingObjects: %v", err)
	}
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
