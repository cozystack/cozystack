// SPDX-License-Identifier: Apache-2.0
package backupcontroller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"testing"
	"time"

	velerov1 "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/scheme"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	strategyv1alpha1 "github.com/cozystack/cozystack/api/backups/strategy/v1alpha1"
	backupsv1alpha1 "github.com/cozystack/cozystack/api/backups/v1alpha1"
)

const (
	k8sTestNS      = "tenant-a"
	k8sTestApp     = "c1"
	k8sTestRelease = "kubernetes-c1"
)

var kubernetesAppGVR = schema.GroupVersionResource{Group: backupsv1alpha1.DefaultApplicationAPIGroup, Version: "v1alpha1", Resource: "kubernetes"}

func testCertPEM(t *testing.T, isCA bool, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             notAfter.Add(-time.Hour),
		NotAfter:              notAfter,
		IsCA:                  isCA,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func unstr(gvk schema.GroupVersionKind, ns, name string, fields map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	for k, v := range fields {
		u.Object[k] = v
	}
	u.SetGroupVersionKind(gvk)
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

func TestKubernetesCertificateWarnings(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	secrets := []corev1.Secret{
		{ObjectMeta: metav1.ObjectMeta{Name: "ca"}, Data: map[string][]byte{
			"ca.crt": testCertPEM(t, true, now.Add(10*24*time.Hour)),
			"ca.key": []byte("not a certificate"),
		}},
		{ObjectMeta: metav1.ObjectMeta{Name: "leaf-soon"}, Data: map[string][]byte{
			"tls.crt": testCertPEM(t, false, now.Add(24*time.Hour)),
		}},
		{ObjectMeta: metav1.ObjectMeta{Name: "leaf-later"}, Data: map[string][]byte{
			"tls.crt": testCertPEM(t, false, now.Add(10*24*time.Hour)),
		}},
	}

	got := kubernetesCertificateWarnings(secrets, now, 30*24*time.Hour, 2*24*time.Hour)
	if len(got) != 2 {
		t.Fatalf("got %d warnings %v, want 2", len(got), got)
	}
	if !strings.HasPrefix(got[0], "CA ca/ca.crt") || !strings.HasPrefix(got[1], "certificate leaf-soon/tls.crt") {
		t.Fatalf("unexpected warnings %v", got)
	}

	if got := kubernetesCertificateWarnings(secrets, now, time.Hour, time.Hour); len(got) != 0 {
		t.Fatalf("want no warnings inside a short window, got %v", got)
	}
}

func TestKubernetesHelmReleaseNames(t *testing.T) {
	mdGVK := schema.GroupVersionKind{Group: "cluster.x-k8s.io", Version: "v1beta1", Kind: "MachineDeployment"}
	md := func(name, cluster, owner string) unstructured.Unstructured {
		u := unstr(mdGVK, k8sTestNS, name, map[string]any{"spec": map[string]any{"clusterName": cluster}})
		u.SetAnnotations(map[string]string{helmReleaseNameAnnotation: owner})
		return *u
	}
	got := kubernetesHelmReleaseNames([]unstructured.Unstructured{
		md("a", k8sTestRelease, "kubernetes-nodes-c1-a"),
		md("b", k8sTestRelease, "kubernetes-nodes-c1-b"),
		md("legacy", k8sTestRelease, k8sTestRelease),
		md("other", "kubernetes-c2", "kubernetes-nodes-c2-a"),
	}, k8sTestRelease)
	want := []string{k8sTestRelease, "kubernetes-nodes-c1-a", "kubernetes-nodes-c1-b"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseKubernetesRestoreOptions(t *testing.T) {
	o, err := parseKubernetesRestoreOptions(nil)
	if err != nil || !o.restoreEtcd() || !o.restoreVolumes() {
		t.Fatalf("defaults: %+v %v", o, err)
	}
	o, err = parseKubernetesRestoreOptions(&runtime.RawExtension{Raw: []byte(`{"volumes":false}`)})
	if err != nil || !o.restoreEtcd() || o.restoreVolumes() {
		t.Fatalf("etcd only: %+v %v", o, err)
	}
	if _, err := parseKubernetesRestoreOptions(&runtime.RawExtension{Raw: []byte(`{"etcd":false,"volumes":false}`)}); err == nil {
		t.Fatal("want an error when both halves are disabled")
	}
}

func TestKubernetesVeleroRestoreSpecPhases(t *testing.T) {
	strategy := &strategyv1alpha1.Kubernetes{}
	backup := &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: k8sTestNS},
		Spec:       backupsv1alpha1.BackupSpec{ApplicationRef: corev1.TypedLocalObjectReference{Kind: kubernetesAppKind, Name: k8sTestApp}},
	}
	secrets, err := kubernetesVeleroRestoreSpec(strategy, backup, kubernetesPhaseSecrets, k8sTestRelease)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(secrets.IncludedResources, ",") != "secrets" || len(secrets.OrLabelSelectors) != 2 {
		t.Fatalf("secrets phase: %+v", secrets)
	}
	volumes, err := kubernetesVeleroRestoreSpec(strategy, backup, kubernetesPhaseVolumes, k8sTestRelease)
	if err != nil {
		t.Fatal(err)
	}
	if volumes.OrLabelSelectors[0].MatchLabels[clusterNameLabel] != k8sTestRelease || volumes.RestorePVs == nil || !*volumes.RestorePVs {
		t.Fatalf("volumes phase: %+v", volumes)
	}
	if _, err := kubernetesVeleroRestoreSpec(strategy, backup, "bogus", k8sTestRelease); err == nil {
		t.Fatal("want an error for an unknown phase")
	}
}

type kubernetesTestEnv struct {
	backup  *BackupJobReconciler
	restore *RestoreJobReconciler
	client  client.Client
}

// newKubernetesTestEnv seeds a live tenant cluster: the application, its
// HelmReleases, TenantControlPlane, DataStore, KamajiControlPlane, one worker
// pool and one data DataVolume.
func newKubernetesTestEnv(t *testing.T, objs ...client.Object) *kubernetesTestEnv {
	t.Helper()
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = backupsv1alpha1.AddToScheme(s)
	_ = strategyv1alpha1.AddToScheme(s)
	_ = velerov1.AddToScheme(s)

	listKinds := map[schema.GroupVersionResource]string{
		kubernetesAppGVR:      "KubernetesList",
		helmReleaseGVR:        "HelmReleaseList",
		tenantControlPlaneGVR: "TenantControlPlaneList",
		kamajiDataStoreGVR:    "DataStoreList",
		kamajiControlPlaneGVR: "KamajiControlPlaneList",
		machineDeploymentGVR:  "MachineDeploymentList",
		machineGVR:            "MachineList",
		dataVolumeGVR:         "DataVolumeList",
		deploymentGVR:         "DeploymentList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(s, listKinds)
	seed := func(gvr schema.GroupVersionResource, u *unstructured.Unstructured) {
		if err := dyn.Tracker().Create(gvr, u, u.GetNamespace()); err != nil {
			t.Fatalf("seed %s: %v", u.GetName(), err)
		}
	}
	appGVK := schema.GroupVersionKind{Group: backupsv1alpha1.DefaultApplicationAPIGroup, Version: "v1alpha1", Kind: kubernetesAppKind}
	seed(kubernetesAppGVR, unstr(appGVK, k8sTestNS, k8sTestApp, map[string]any{"spec": map[string]any{}}))
	hrGVK := schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"}
	for _, name := range []string{k8sTestRelease, "kubernetes-nodes-c1-md0"} {
		hr := unstr(hrGVK, k8sTestNS, name, map[string]any{"spec": map[string]any{"interval": "5m"}})
		hr.SetLabels(map[string]string{"apps.cozystack.io/application.name": name, "velero.io/backup-name": "old"})
		seed(helmReleaseGVR, hr)
	}
	seed(tenantControlPlaneGVR, unstr(schema.GroupVersionKind{Group: "kamaji.clastix.io", Version: "v1alpha1", Kind: "TenantControlPlane"}, k8sTestNS, k8sTestRelease, map[string]any{
		"status": map[string]any{"storage": map[string]any{
			"driver":        "etcd",
			"dataStoreName": "tenant-a",
			"setup":         map[string]any{"schema": "tenant_a_kubernetes_c1"},
			"certificate":   map[string]any{"secretName": k8sTestRelease + "-datastore-certificate"},
		}},
	}))
	seed(kamajiDataStoreGVR, unstr(schema.GroupVersionKind{Group: "kamaji.clastix.io", Version: "v1alpha1", Kind: "DataStore"}, "", "tenant-a", map[string]any{
		"spec": map[string]any{"endpoints": []any{"etcd.tenant-a.svc:2379"}},
	}))
	seed(kamajiControlPlaneGVR, unstr(schema.GroupVersionKind{Group: "controlplane.cluster.x-k8s.io", Version: "v1alpha1", Kind: "KamajiControlPlane"}, k8sTestNS, k8sTestRelease, map[string]any{
		"spec": map[string]any{"replicas": int64(2)},
	}))
	md := unstr(schema.GroupVersionKind{Group: "cluster.x-k8s.io", Version: "v1beta1", Kind: "MachineDeployment"}, k8sTestNS, k8sTestRelease+"-md0", map[string]any{
		"spec": map[string]any{"clusterName": k8sTestRelease, "replicas": int64(3)},
	})
	md.SetAnnotations(map[string]string{helmReleaseNameAnnotation: "kubernetes-nodes-c1-md0"})
	seed(machineDeploymentGVR, md)
	dv := unstr(schema.GroupVersionKind{Group: "cdi.kubevirt.io", Version: "v1beta1", Kind: "DataVolume"}, k8sTestNS, "pvc-1234", nil)
	dv.SetLabels(map[string]string{clusterNameLabel: k8sTestRelease})
	seed(dataVolumeGVR, dv)
	seed(deploymentGVR, unstr(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, k8sTestNS, k8sTestRelease+"-cluster-autoscaler", map[string]any{
		"spec": map[string]any{"replicas": int64(1)},
	}))

	base := []client.Object{
		&velerov1.Backup{
			ObjectMeta: metav1.ObjectMeta{Namespace: veleroNamespace, Name: "tenant-a.bj1-xyz"},
			Status:     velerov1.BackupStatus{Phase: velerov1.BackupPhaseCompleted},
		},
		&strategyv1alpha1.Kubernetes{
			ObjectMeta: metav1.ObjectMeta{Name: "cozy-default-kubernetes"},
			Spec: strategyv1alpha1.KubernetesSpec{
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name:  "etcd",
					Image: "img",
					Args:  []string{"{{ .Mode }} {{ .ObjectKey }} {{ .Datastore.Prefix }} {{ .Datastore.Endpoints }} {{ .Datastore.CertificateSecret }} {{ .Datastore.SourcePrefix }}"},
				}}}},
				Velero: strategyv1alpha1.VeleroTemplate{Spec: velerov1.BackupSpec{
					IncludedNamespaces: []string{"{{ .Application.metadata.namespace }}"},
					TTL:                metav1.Duration{Duration: 720 * time.Hour},
				}},
			},
		},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: k8sTestNS, Name: k8sTestRelease + "-talos-ca"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: k8sTestNS, Name: k8sTestRelease + "-ca"}, Data: map[string][]byte{
			"ca.crt": testCertPEM(t, true, time.Now().Add(24*time.Hour)),
		}},
	}
	c := clientfake.NewClientBuilder().WithScheme(s).
		WithObjects(append(base, objs...)...).
		WithStatusSubresource(&backupsv1alpha1.BackupJob{}, &backupsv1alpha1.RestoreJob{}, &backupsv1alpha1.Backup{}).
		Build()
	mapper := &mockRESTMapper{mapping: &meta.RESTMapping{Resource: kubernetesAppGVR, GroupVersionKind: appGVK, Scope: meta.RESTScopeNamespace}}
	return &kubernetesTestEnv{
		backup:  &BackupJobReconciler{Client: c, Interface: dyn, RESTMapper: mapper, Scheme: s, Recorder: record.NewFakeRecorder(20)},
		restore: &RestoreJobReconciler{Client: c, Interface: dyn, RESTMapper: mapper, Scheme: s, Recorder: record.NewFakeRecorder(20)},
		client:  c,
	}
}

func k8sResolved() *ResolvedBackupConfig {
	return &ResolvedBackupConfig{
		StrategyRef: corev1.TypedLocalObjectReference{APIGroup: new("strategy.backups.cozystack.io"), Kind: strategyv1alpha1.KubernetesStrategyKind, Name: "cozy-default-kubernetes"},
		Parameters:  map[string]string{"p": "v"},
	}
}

func TestReconcileKubernetesBackup(t *testing.T) {
	ctx := context.Background()
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: k8sTestNS, Name: "bj1"},
		Spec: backupsv1alpha1.BackupJobSpec{ApplicationRef: corev1.TypedLocalObjectReference{
			APIGroup: new(backupsv1alpha1.DefaultApplicationAPIGroup), Kind: kubernetesAppKind, Name: k8sTestApp,
		}},
	}
	env := newKubernetesTestEnv(t, bj)
	get := func() *backupsv1alpha1.BackupJob {
		out := &backupsv1alpha1.BackupJob{}
		if err := env.client.Get(ctx, client.ObjectKeyFromObject(bj), out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	step := func() {
		t.Helper()
		if _, err := env.backup.reconcileKubernetes(ctx, get(), k8sResolved()); err != nil {
			t.Fatal(err)
		}
	}

	step() // StartedAt
	step() // certificates
	cond := meta.FindStatusCondition(get().Status.Conditions, kubernetesCondCertificates)
	if cond == nil || cond.Status != metav1.ConditionTrue || !strings.Contains(cond.Message, k8sTestRelease+"-ca/ca.crt") {
		t.Fatalf("certificate condition: %+v", cond)
	}

	step() // dump Job
	job := &batchv1.Job{}
	if err := env.client.Get(ctx, client.ObjectKey{Namespace: k8sTestNS, Name: "bj1-backup"}, job); err != nil {
		t.Fatalf("dump Job: %v", err)
	}
	args := job.Spec.Template.Spec.Containers[0].Args[0]
	want := "backup tenant-a/c1/bj1.etcd.json tenant_a_kubernetes_c1 https://etcd.tenant-a.svc:2379 kubernetes-c1-datastore-certificate tenant_a_kubernetes_c1"
	if args != want {
		t.Fatalf("rendered args %q, want %q", args, want)
	}
	step()
	vbs := &velerov1.BackupList{}
	_ = env.client.List(ctx, vbs, client.MatchingLabels{backupsv1alpha1.OwningJobNameLabel: "bj1"})
	if len(vbs.Items) != 0 {
		t.Fatal("Velero Backup created before the dump Job completed")
	}

	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := env.client.Status().Update(ctx, job); err != nil {
		t.Fatal(err)
	}
	// A claim the CSI driver created without a DataVolume is still one an
	// in-place restore has to clear before Velero writes it back.
	if err := env.client.Create(ctx, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Namespace: k8sTestNS, Name: "pvc-plain", Labels: map[string]string{clusterNameLabel: k8sTestRelease},
	}}); err != nil {
		t.Fatal(err)
	}
	step()
	if err := env.client.List(ctx, vbs, client.MatchingLabels{backupsv1alpha1.OwningJobNameLabel: "bj1"}); err != nil || len(vbs.Items) != 1 {
		t.Fatalf("Velero Backups: %d %v", len(vbs.Items), err)
	}
	// A volume created while Velero runs may be missing from its backup, so a
	// restore of this backup must never delete it.
	late := unstr(schema.GroupVersionKind{Group: "cdi.kubevirt.io", Version: "v1beta1", Kind: "DataVolume"}, k8sTestNS, "pvc-late", nil)
	late.SetLabels(map[string]string{clusterNameLabel: k8sTestRelease})
	if _, err := env.backup.Interface.Resource(dataVolumeGVR).Namespace(k8sTestNS).Create(ctx, late, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if ns := vbs.Items[0].Spec.IncludedNamespaces; len(ns) != 1 || ns[0] != k8sTestNS {
		t.Fatalf("Velero spec not rendered: %v", ns)
	}
	talosCA := &corev1.Secret{}
	_ = env.client.Get(ctx, client.ObjectKey{Namespace: k8sTestNS, Name: k8sTestRelease + "-talos-ca"}, talosCA)
	if talosCA.Labels[helmInstanceLabel] != k8sTestRelease {
		t.Fatalf("talos CA not labelled for the Velero selector: %v", talosCA.Labels)
	}

	vb := vbs.Items[0]
	vb.Status.Phase = velerov1.BackupPhaseCompleted
	if err := env.client.Update(ctx, &vb); err != nil {
		t.Fatal(err)
	}
	// A pool that is suspended while the backup runs must not come back
	// suspended when the cluster is rebuilt from it.
	if _, err := env.backup.Interface.Resource(helmReleaseGVR).Namespace(k8sTestNS).Patch(ctx, "kubernetes-nodes-c1-md0", types.MergePatchType, []byte(`{"spec":{"suspend":true}}`), metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
	step()
	if got := get(); got.Status.Phase != backupsv1alpha1.BackupJobPhaseSucceeded || got.Status.BackupRef == nil {
		t.Fatalf("BackupJob not succeeded: %+v", got.Status)
	}
	backup := &backupsv1alpha1.Backup{}
	if err := env.client.Get(ctx, client.ObjectKey{Namespace: k8sTestNS, Name: "bj1"}, backup); err != nil {
		t.Fatal(err)
	}
	md := backup.Spec.DriverMetadata
	if md[veleroBackupNameMetadataKey] != vb.Name || md[kubernetesDataVolumesMetaKey] != "pvc-1234,pvc-plain" ||
		md[kubernetesDatastorePrefixMetaKey] != "tenant_a_kubernetes_c1" || md[kubernetesParamPrefix+"p"] != "v" ||
		md[kubernetesCertWarningsMetaKey] == "" {
		t.Fatalf("driver metadata: %v", md)
	}
	releases, err := kubernetesBackupHelmReleases(backup)
	if err != nil || len(releases) != 2 {
		t.Fatalf("recorded HelmReleases: %v", releases)
	}
	raw, _ := json.Marshal(releases)
	if strings.Contains(string(raw), "velero.io/") || strings.Contains(string(raw), "suspend") {
		t.Fatalf("Velero labels or the suspend flag leaked into the recorded HelmReleases: %s", raw)
	}
}

func k8sBackupForRestore() *backupsv1alpha1.Backup {
	return &backupsv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: k8sTestNS, Name: "b1"},
		Spec: backupsv1alpha1.BackupSpec{
			ApplicationRef: corev1.TypedLocalObjectReference{APIGroup: new(backupsv1alpha1.DefaultApplicationAPIGroup), Kind: kubernetesAppKind, Name: k8sTestApp},
			StrategyRef:    k8sResolved().StrategyRef,
			DriverMetadata: map[string]string{
				veleroBackupNameMetadataKey:         "tenant-a.bj1-xyz",
				kubernetesObjectMetaKey:             "tenant-a/c1/bj1.etcd.json",
				kubernetesDatastorePrefixMetaKey:    "tenant_a_kubernetes_c1",
				kubernetesDatastoreEndpointsMetaKey: "https://etcd.tenant-a.svc:2379",
				kubernetesDataVolumesMetaKey:        "pvc-1234",
				kubernetesHelmReleasesMetaKey:       `[{"apiVersion":"helm.toolkit.fluxcd.io/v2","kind":"HelmRelease","metadata":{"name":"kubernetes-c1"},"spec":{"interval":"5m"}}]`,
			},
		},
	}
}

// k8sRestoreRun drives one RestoreJob through the test environment.
type k8sRestoreRun struct {
	t      *testing.T
	env    *kubernetesTestEnv
	rj     *backupsv1alpha1.RestoreJob
	backup *backupsv1alpha1.Backup
}

func newK8sRestoreRun(t *testing.T, options string, backup *backupsv1alpha1.Backup) *k8sRestoreRun {
	t.Helper()
	rj := &backupsv1alpha1.RestoreJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: k8sTestNS, Name: "rj"},
		Spec:       backupsv1alpha1.RestoreJobSpec{BackupRef: corev1.LocalObjectReference{Name: backup.Name}},
	}
	if options != "" {
		rj.Spec.Options = &runtime.RawExtension{Raw: []byte(options)}
	}
	return &k8sRestoreRun{t: t, env: newKubernetesTestEnv(t, backup, rj), rj: rj, backup: backup}
}

func (r *k8sRestoreRun) get() *backupsv1alpha1.RestoreJob {
	r.t.Helper()
	out := &backupsv1alpha1.RestoreJob{}
	if err := r.env.client.Get(context.Background(), client.ObjectKeyFromObject(r.rj), out); err != nil {
		r.t.Fatal(err)
	}
	return out
}

func (r *k8sRestoreRun) step() {
	r.t.Helper()
	if _, err := r.env.restore.reconcileKubernetesRestore(context.Background(), r.get(), r.backup); err != nil {
		r.t.Fatal(err)
	}
}

func (r *k8sRestoreRun) finishJob(name string, condType batchv1.JobConditionType) {
	r.t.Helper()
	job := &batchv1.Job{}
	if err := r.env.client.Get(context.Background(), client.ObjectKey{Namespace: k8sTestNS, Name: name}, job); err != nil {
		r.t.Fatalf("Job %s: %v", name, err)
	}
	job.Status.Conditions = []batchv1.JobCondition{{Type: condType, Status: corev1.ConditionTrue, Message: "boom"}}
	if err := r.env.client.Status().Update(context.Background(), job); err != nil {
		r.t.Fatal(err)
	}
}

func (r *k8sRestoreRun) finishVeleroRestore(phase velerov1.RestorePhase) {
	r.t.Helper()
	list := &velerov1.RestoreList{}
	if err := r.env.client.List(context.Background(), list); err != nil || len(list.Items) == 0 {
		r.t.Fatalf("Velero Restores: %v %v", len(list.Items), err)
	}
	vr := list.Items[len(list.Items)-1]
	vr.Status.Phase = phase
	if err := r.env.client.Update(context.Background(), &vr); err != nil {
		r.t.Fatal(err)
	}
}

func (r *k8sRestoreRun) obj(gvr schema.GroupVersionResource, name string) *unstructured.Unstructured {
	r.t.Helper()
	u, err := r.env.restore.Interface.Resource(gvr).Namespace(k8sTestNS).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		r.t.Fatalf("get %s %s: %v", gvr.Resource, name, err)
	}
	return u
}

func (r *k8sRestoreRun) replicas(gvr schema.GroupVersionResource, name string) (int64, bool) {
	n, found, _ := unstructured.NestedInt64(r.obj(gvr, name).Object, "spec", "replicas")
	return n, found
}

func (r *k8sRestoreRun) suspended(name string) bool {
	s, _, _ := unstructured.NestedBool(r.obj(helmReleaseGVR, name).Object, "spec", "suspend")
	return s
}

func (r *k8sRestoreRun) addMachine(name string) {
	r.t.Helper()
	m := unstr(schema.GroupVersionKind{Group: "cluster.x-k8s.io", Version: "v1beta1", Kind: "Machine"}, k8sTestNS, name, nil)
	m.SetLabels(map[string]string{clusterNameLabel: k8sTestRelease})
	if _, err := r.env.restore.Interface.Resource(machineGVR).Namespace(k8sTestNS).Create(context.Background(), m, metav1.CreateOptions{}); err != nil {
		r.t.Fatal(err)
	}
}

// verifyAndCapture runs the preflight and the mode decision.
func (r *k8sRestoreRun) verifyAndCapture(etcd bool) {
	r.t.Helper()
	r.step() // StartedAt
	r.step() // preflight
	if etcd {
		r.finishJob("rj-verify", batchv1.JobComplete)
		r.step()
	}
	if !meta.IsStatusConditionTrue(r.get().Status.Conditions, kubernetesCondVerified) {
		r.t.Fatalf("backup not verified: %+v", r.get().Status)
	}
	r.step() // capture
}

func TestReconcileKubernetesRestoreInPlaceDefault(t *testing.T) {
	run := newK8sRestoreRun(t, "", k8sBackupForRestore())
	run.addMachine("w-1")
	run.verifyAndCapture(true)

	run.step() // stop: a worker is still there
	if n, _ := run.replicas(kamajiControlPlaneGVR, k8sTestRelease); n != 2 {
		t.Fatal("control plane scaled down while a worker Machine still needs it to delete its Node")
	}
	if n, _ := run.replicas(machineDeploymentGVR, k8sTestRelease+"-md0"); n != 0 {
		t.Fatalf("worker pool not scaled down: %d", n)
	}
	if n, _ := run.replicas(deploymentGVR, k8sTestRelease+"-cluster-autoscaler"); n != 0 {
		t.Fatalf("autoscaler not scaled down: %d", n)
	}
	if _, ok := run.obj(machineGVR, "w-1").GetAnnotations()[machineExcludeDrainAnnotation]; !ok {
		t.Fatal("Machine not released from draining")
	}
	if c := meta.FindStatusCondition(run.get().Status.Conditions, kubernetesCondStopped); c == nil || c.Status != metav1.ConditionFalse || !strings.Contains(c.Message, "Machine") {
		t.Fatalf("stop progress not reported: %+v", c)
	}
	if _, err := run.env.restore.Interface.Resource(dataVolumeGVR).Namespace(k8sTestNS).Get(context.Background(), "pvc-1234", metav1.GetOptions{}); err != nil {
		t.Fatal("data DataVolume deleted while a worker may still mount it")
	}

	if err := run.env.restore.Interface.Resource(machineGVR).Namespace(k8sTestNS).Delete(context.Background(), "w-1", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	run.step() // stop settles
	if _, err := run.env.restore.Interface.Resource(dataVolumeGVR).Namespace(k8sTestNS).Get(context.Background(), "pvc-1234", metav1.GetOptions{}); err == nil {
		t.Fatal("backed-up data DataVolume not deleted once the workers were gone")
	}
	if n, _ := run.replicas(kamajiControlPlaneGVR, k8sTestRelease); n != 0 {
		t.Fatal("control plane not scaled down after the workers were gone")
	}
	if !meta.IsStatusConditionTrue(run.get().Status.Conditions, kubernetesCondStopped) {
		t.Fatal("cluster not reported stopped")
	}

	run.step() // etcd Job
	run.finishJob("rj-restore", batchv1.JobComplete)
	run.step() // etcd done
	run.step() // volumes Velero Restore
	run.finishVeleroRestore(velerov1.RestorePhaseCompleted)
	run.step() // volumes done
	run.step() // resume

	if got := run.get(); got.Status.Phase != backupsv1alpha1.RestoreJobPhaseSucceeded {
		t.Fatalf("RestoreJob not succeeded: %+v", got.Status)
	}
	for _, c := range []struct {
		gvr  schema.GroupVersionResource
		name string
		want int64
	}{
		{kamajiControlPlaneGVR, k8sTestRelease, 2},
		{machineDeploymentGVR, k8sTestRelease + "-md0", 3},
		{deploymentGVR, k8sTestRelease + "-cluster-autoscaler", 1},
	} {
		if n, _ := run.replicas(c.gvr, c.name); n != c.want {
			t.Errorf("%s %s replicas = %d, want %d", c.gvr.Resource, c.name, n, c.want)
		}
		if _, left := run.obj(c.gvr, c.name).GetAnnotations()[kubernetesReplicasBeforeAnnotation]; left {
			t.Errorf("%s %s keeps the restore's replica record", c.gvr.Resource, c.name)
		}
	}
	if run.suspended(k8sTestRelease) || run.suspended("kubernetes-nodes-c1-md0") {
		t.Fatal("HelmRelease left suspended")
	}
}

func TestReconcileKubernetesRestoreInPlaceEtcdOnly(t *testing.T) {
	run := newK8sRestoreRun(t, `{"volumes":false}`, k8sBackupForRestore())
	run.verifyAndCapture(true)
	run.step() // stop
	if n, _ := run.replicas(kamajiControlPlaneGVR, k8sTestRelease); n != 0 {
		t.Fatalf("control plane not scaled down: %d", n)
	}
	if n, _ := run.replicas(machineDeploymentGVR, k8sTestRelease+"-md0"); n != 3 {
		t.Fatalf("workers touched by an etcd-only restore: %d", n)
	}
	run.step()
	run.finishJob("rj-restore", batchv1.JobComplete)
	run.step()
	run.step()
	if got := run.get(); got.Status.Phase != backupsv1alpha1.RestoreJobPhaseSucceeded {
		t.Fatalf("RestoreJob not succeeded: %+v", got.Status)
	}
	if n, _ := run.replicas(kamajiControlPlaneGVR, k8sTestRelease); n != 2 {
		t.Fatalf("control plane replicas not restored: %d", n)
	}
	list := &velerov1.RestoreList{}
	_ = run.env.client.List(context.Background(), list)
	if len(list.Items) != 0 {
		t.Fatal("an etcd-only restore created a Velero Restore")
	}
}

func TestReconcileKubernetesRestoreKeepsPreSuspendedRelease(t *testing.T) {
	run := newK8sRestoreRun(t, `{"volumes":false}`, k8sBackupForRestore())
	patch := []byte(`{"spec":{"suspend":true}}`)
	if _, err := run.env.restore.Interface.Resource(helmReleaseGVR).Namespace(k8sTestNS).Patch(context.Background(), "kubernetes-nodes-c1-md0", types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
	run.verifyAndCapture(true)
	run.step()
	run.step()
	run.finishJob("rj-restore", batchv1.JobComplete)
	run.step()
	run.step()
	if run.get().Status.Phase != backupsv1alpha1.RestoreJobPhaseSucceeded {
		t.Fatalf("RestoreJob not succeeded: %+v", run.get().Status)
	}
	if !run.suspended("kubernetes-nodes-c1-md0") {
		t.Fatal("a HelmRelease suspended before the restore was resumed by it")
	}
	if run.suspended(k8sTestRelease) {
		t.Fatal("the cluster HelmRelease the restore suspended was left suspended")
	}
}

func TestReconcileKubernetesRestoreEtcdFailureLeavesControlPlaneStopped(t *testing.T) {
	run := newK8sRestoreRun(t, `{"volumes":false}`, k8sBackupForRestore())
	run.verifyAndCapture(true)
	run.step()
	run.step()
	run.addJobPod("rj-restore", kubernetesJobWriting)
	run.finishJob("rj-restore", batchv1.JobFailed)
	run.step()
	got := run.get()
	if got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed || !meta.IsStatusConditionTrue(got.Status.Conditions, kubernetesCondEtcdWriteFailed) {
		t.Fatalf("want a failed restore with the etcd write marked failed: %+v", got.Status)
	}
	if n, _ := run.replicas(kamajiControlPlaneGVR, k8sTestRelease); n != 0 {
		t.Fatal("control plane brought back up on a possibly partial etcd prefix")
	}
	if !strings.Contains(got.Status.Message+fmtConditions(got), ".pre-restore") {
		t.Fatalf("failure does not point at the pre-restore copy: %+v", got.Status)
	}

	run.env.restore.cleanupKubernetesRestore(context.Background(), got)
	if n, _ := run.replicas(kamajiControlPlaneGVR, k8sTestRelease); n != 0 {
		t.Fatal("deleting the RestoreJob brought the control plane up on a possibly partial prefix")
	}
}

func fmtConditions(rj *backupsv1alpha1.RestoreJob) string {
	var b strings.Builder
	for _, c := range rj.Status.Conditions {
		b.WriteString(c.Message)
	}
	return b.String()
}

func TestCleanupKubernetesRestoreResumesStoppedCluster(t *testing.T) {
	run := newK8sRestoreRun(t, "", k8sBackupForRestore())
	run.verifyAndCapture(true)
	run.step() // stop: workers gone, volumes deleted, control plane stopped
	if n, _ := run.replicas(kamajiControlPlaneGVR, k8sTestRelease); n != 0 {
		t.Fatalf("control plane not stopped: %d", n)
	}
	run.env.restore.cleanupKubernetesRestore(context.Background(), run.get())
	if n, _ := run.replicas(kamajiControlPlaneGVR, k8sTestRelease); n != 2 {
		t.Fatalf("deleting the RestoreJob left the control plane at %d replicas", n)
	}
	if n, _ := run.replicas(machineDeploymentGVR, k8sTestRelease+"-md0"); n != 3 {
		t.Fatalf("deleting the RestoreJob left the worker pool at %d replicas", n)
	}
	if run.suspended(k8sTestRelease) {
		t.Fatal("deleting the RestoreJob left the HelmRelease suspended")
	}
}

func TestReconcileKubernetesRestoreStopDeadline(t *testing.T) {
	run := newK8sRestoreRun(t, "", k8sBackupForRestore())
	run.addMachine("w-1")
	run.verifyAndCapture(true)
	run.step() // stop starts, a worker remains
	rj := run.get()
	cond := meta.FindStatusCondition(rj.Status.Conditions, kubernetesCondStopped)
	cond.LastTransitionTime = metav1.NewTime(time.Now().Add(-restorePreparationDeadline - time.Minute))
	if err := run.env.client.Status().Update(context.Background(), rj); err != nil {
		t.Fatal(err)
	}
	run.step()
	if got := run.get(); got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("a stop that never settles must fail the restore: %+v", got.Status)
	}
	if n, _ := run.replicas(machineDeploymentGVR, k8sTestRelease+"-md0"); n != 3 {
		t.Fatalf("worker pool not resumed after the stop timed out: %d", n)
	}
	if run.suspended(k8sTestRelease) {
		t.Fatal("HelmRelease left suspended after the stop timed out")
	}
}

func TestReconcileKubernetesRestoreVerifiesBeforeChangingAnything(t *testing.T) {
	backup := k8sBackupForRestore()
	backup.Spec.DriverMetadata[veleroBackupNameMetadataKey] = "expired"
	run := newK8sRestoreRun(t, "", backup)
	run.step()
	run.step()
	if got := run.get(); got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("a missing Velero backup must fail the restore: %+v", got.Status)
	}
	if run.suspended(k8sTestRelease) {
		t.Fatal("cluster touched before the backup was verified")
	}
	if _, err := run.env.restore.Interface.Resource(dataVolumeGVR).Namespace(k8sTestNS).Get(context.Background(), "pvc-1234", metav1.GetOptions{}); err != nil {
		t.Fatal("data DataVolume deleted before the backup was verified")
	}

	backup = k8sBackupForRestore()
	backup.Spec.DriverMetadata[veleroBackupNameMetadataKey] = "partial"
	run = newK8sRestoreRun(t, "", backup)
	if err := run.env.client.Create(context.Background(), &velerov1.Backup{
		ObjectMeta: metav1.ObjectMeta{Namespace: veleroNamespace, Name: "partial"},
		Status:     velerov1.BackupStatus{Phase: velerov1.BackupPhasePartiallyFailed},
	}); err != nil {
		t.Fatal(err)
	}
	run.step()
	run.step()
	if got := run.get(); got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("a Velero backup that did not complete must fail the restore: %+v", got.Status)
	}

	run = newK8sRestoreRun(t, "", k8sBackupForRestore())
	run.step()
	run.step()
	run.finishJob("rj-verify", batchv1.JobFailed)
	run.step()
	if got := run.get(); got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("a missing etcd dump must fail the restore: %+v", got.Status)
	}
	if run.suspended(k8sTestRelease) {
		t.Fatal("cluster touched before the dump was verified")
	}
}

func k8sBackupOfDeletedCluster() *backupsv1alpha1.Backup {
	backup := k8sBackupForRestore()
	backup.Spec.ApplicationRef.Name = "gone"
	backup.Spec.DriverMetadata[kubernetesHelmReleasesMetaKey] = `[` +
		`{"apiVersion":"helm.toolkit.fluxcd.io/v2","kind":"HelmRelease","metadata":{"name":"kubernetes-gone"},"spec":{"interval":"5m"}},` +
		`{"apiVersion":"helm.toolkit.fluxcd.io/v2","kind":"HelmRelease","metadata":{"name":"kubernetes-nodes-gone-md0"},"spec":{"interval":"5m"}}]`
	return backup
}

// TestReconcileKubernetesRestoreRecreate rebuilds a deleted cluster whose new
// TenantControlPlane gets a fresh, UID-keyed schema: the dump must be written
// under that schema, and the worker pools come back only after it is.
func TestReconcileKubernetesRestoreRecreate(t *testing.T) {
	ctx := context.Background()
	run := newK8sRestoreRun(t, "", k8sBackupOfDeletedCluster())
	run.verifyAndCapture(true)
	if state := kubernetesStateFromRestoreJob(run.get()); state == nil || state.Mode != kubernetesRestoreModeRecreate || state.Release != "kubernetes-gone" {
		t.Fatalf("want recreate mode, got %+v", state)
	}
	run.step() // secrets
	run.finishVeleroRestore(velerov1.RestorePhaseCompleted)
	run.step()
	run.step() // volumes
	run.finishVeleroRestore(velerov1.RestorePhaseCompleted)
	run.step()

	run.step() // cluster HelmRelease, waiting for the TenantControlPlane
	dyn := run.env.restore.Interface
	if _, err := dyn.Resource(helmReleaseGVR).Namespace(k8sTestNS).Get(ctx, "kubernetes-gone", metav1.GetOptions{}); err != nil {
		t.Fatalf("cluster HelmRelease not recreated: %v", err)
	}
	if _, err := dyn.Resource(helmReleaseGVR).Namespace(k8sTestNS).Get(ctx, "kubernetes-nodes-gone-md0", metav1.GetOptions{}); err == nil {
		t.Fatal("worker pool recreated before the etcd state was written back")
	}
	if c := meta.FindStatusCondition(run.get().Status.Conditions, kubernetesCondRecreated); c == nil || c.Status != metav1.ConditionFalse {
		t.Fatalf("wait for the new control plane not reported: %+v", c)
	}

	const newSchema = "0f2c1e4a-77b1-4c39-9a3e-6d1f2b9c8e10"
	if _, err := dyn.Resource(tenantControlPlaneGVR).Namespace(k8sTestNS).Create(ctx, unstr(schema.GroupVersionKind{Group: "kamaji.clastix.io", Version: "v1alpha1", Kind: "TenantControlPlane"}, k8sTestNS, "kubernetes-gone", map[string]any{
		"status": map[string]any{"storage": map[string]any{"driver": "etcd", "dataStoreName": "tenant-a", "setup": map[string]any{"schema": newSchema}}},
	}), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := dyn.Resource(kamajiControlPlaneGVR).Namespace(k8sTestNS).Create(ctx, unstr(schema.GroupVersionKind{Group: "controlplane.cluster.x-k8s.io", Version: "v1alpha1", Kind: "KamajiControlPlane"}, k8sTestNS, "kubernetes-gone", map[string]any{
		"spec": map[string]any{"replicas": int64(2)},
	}), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	run.step() // control plane is up
	run.step() // stop it for the etcd write
	if n, _ := run.replicas(kamajiControlPlaneGVR, "kubernetes-gone"); n != 0 {
		t.Fatalf("new control plane not stopped before the etcd write: %d", n)
	}
	run.step() // etcd Job
	job := &batchv1.Job{}
	if err := run.env.client.Get(ctx, client.ObjectKey{Namespace: k8sTestNS, Name: "rj-restore"}, job); err != nil {
		t.Fatal(err)
	}
	args := job.Spec.Template.Spec.Containers[0].Args[0]
	if !strings.Contains(args, " "+newSchema+" ") || !strings.HasSuffix(args, " tenant_a_kubernetes_c1") || !strings.Contains(args, "kubernetes-gone-datastore-certificate") {
		t.Fatalf("restore Job does not write the dump under the new cluster's schema: %q", args)
	}
	run.finishJob("rj-restore", batchv1.JobComplete)
	run.step()
	run.step() // resume, worker pools
	if got := run.get(); got.Status.Phase != backupsv1alpha1.RestoreJobPhaseSucceeded {
		t.Fatalf("rebuild not succeeded: %+v", got.Status)
	}
	if n, _ := run.replicas(kamajiControlPlaneGVR, "kubernetes-gone"); n != 2 {
		t.Fatalf("new control plane not started again: %d", n)
	}
	if _, err := dyn.Resource(helmReleaseGVR).Namespace(k8sTestNS).Get(ctx, "kubernetes-nodes-gone-md0", metav1.GetOptions{}); err != nil {
		t.Fatalf("worker pool not recreated: %v", err)
	}
}

func TestReconcileKubernetesRestoreRecreateSecretsFailure(t *testing.T) {
	run := newK8sRestoreRun(t, "", k8sBackupOfDeletedCluster())
	run.verifyAndCapture(true)
	run.step() // secrets restore
	list := &velerov1.RestoreList{}
	if err := run.env.client.List(context.Background(), list); err != nil || len(list.Items) != 1 {
		t.Fatalf("Velero Restores: %d %v", len(list.Items), err)
	}
	vr := list.Items[0]
	if vr.Labels[kubernetesRestorePhaseLabel] != kubernetesPhaseSecrets || vr.Spec.BackupName != "tenant-a.bj1-xyz" || vr.Spec.ResourceModifier == nil {
		t.Fatalf("secrets Restore: %+v", vr)
	}
	cm := &corev1.ConfigMap{}
	if err := run.env.client.Get(context.Background(), client.ObjectKey{Namespace: veleroNamespace, Name: vr.Spec.ResourceModifier.Name}, cm); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cm.Data["resource-modifier-rules.yaml"], "ownerReferences: null") {
		t.Fatalf("modifiers do not strip ownerReferences: %s", cm.Data["resource-modifier-rules.yaml"])
	}
	run.finishVeleroRestore(velerov1.RestorePhaseFailed)
	run.step()
	if got := run.get(); got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("a failed Velero Restore must fail the RestoreJob: %+v", got.Status)
	}
}

func TestReconcileKubernetesRestoreRecreateRejectsBadReleaseRecord(t *testing.T) {
	backup := k8sBackupForRestore()
	backup.Spec.ApplicationRef.Name = "gone"
	backup.Spec.DriverMetadata[kubernetesHelmReleasesMetaKey] = "not json"
	run := newK8sRestoreRun(t, "", backup)
	run.verifyAndCapture(true)
	run.step()
	if got := run.get(); got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("an unreadable HelmRelease record must fail the rebuild: %+v", got.Status)
	}
}

func TestReconcileKubernetesRestoreRejectsRename(t *testing.T) {
	backup := k8sBackupForRestore()
	run := newK8sRestoreRun(t, "", backup)
	rj := run.get()
	rj.Spec.TargetApplicationRef = &corev1.TypedLocalObjectReference{Kind: kubernetesAppKind, Name: "other"}
	if _, err := run.env.restore.reconcileKubernetesRestore(context.Background(), rj, backup); err != nil {
		t.Fatal(err)
	}
	if got := run.get(); got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("restore into another application must fail: %+v", got.Status)
	}
}

func TestResolveKubernetesDatastoreRejectsNonEtcd(t *testing.T) {
	env := newKubernetesTestEnv(t)
	dyn := env.backup.Interface
	tcp, _ := dyn.Resource(tenantControlPlaneGVR).Namespace(k8sTestNS).Get(context.Background(), k8sTestRelease, metav1.GetOptions{})
	_ = unstructured.SetNestedField(tcp.Object, "PostgreSQL", "status", "storage", "driver")
	if _, err := dyn.Resource(tenantControlPlaneGVR).Namespace(k8sTestNS).Update(context.Background(), tcp, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveKubernetesDatastore(context.Background(), dyn, k8sTestNS, k8sTestApp); err == nil {
		t.Fatal("want an error for a non-etcd datastore")
	}
}

func TestCleanupKubernetesBackupGivesUpAfterRepeatedFailures(t *testing.T) {
	ctx := context.Background()
	backup := k8sBackupForRestore()
	backup.Annotations = map[string]string{kubernetesCleanupAttemptsAnnotation: strconv.Itoa(kubernetesCleanupMaxAttempts - 1)}
	failed := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: k8sTestNS, Name: "b1-cleanup"},
		Status:     batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}},
	}
	env := newKubernetesTestEnv(t, backup, failed)
	br := &BackupReconciler{Client: env.client, Scheme: env.backup.Scheme, Recorder: record.NewFakeRecorder(10)}
	res, err := br.cleanupKubernetesBackup(ctx, backup)
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("cleanup keeps retrying after %d failures: %+v", kubernetesCleanupMaxAttempts, res)
	}
}

func (r *k8sRestoreRun) addJobPod(job, message string) {
	r.t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: k8sTestNS, Name: job + "-pod", Labels: map[string]string{"job-name": job}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "etcd",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: message + "\n"}},
		}}},
	}
	if err := r.env.client.Create(context.Background(), pod); err != nil {
		r.t.Fatal(err)
	}
}

func TestReconcileKubernetesRestoreFailureBeforeWriteResumes(t *testing.T) {
	run := newK8sRestoreRun(t, `{"volumes":false}`, k8sBackupForRestore())
	run.verifyAndCapture(true)
	run.step() // stop
	run.step() // etcd Job
	run.addJobPod("rj-restore", "")
	run.finishJob("rj-restore", batchv1.JobFailed)
	run.step()
	got := run.get()
	if got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed || meta.IsStatusConditionTrue(got.Status.Conditions, kubernetesCondEtcdWriteFailed) {
		t.Fatalf("a Job that never touched the prefix must fail without marking it partial: %+v", got.Status)
	}
	if n, _ := run.replicas(kamajiControlPlaneGVR, k8sTestRelease); n != 2 {
		t.Fatalf("control plane left stopped although the prefix was never changed: %d", n)
	}
}

func TestCleanupKubernetesRestoreKeepsClusterStoppedWhileRestoreJobRuns(t *testing.T) {
	run := newK8sRestoreRun(t, `{"volumes":false}`, k8sBackupForRestore())
	run.verifyAndCapture(true)
	run.step() // stop
	run.step() // etcd Job, still running
	run.env.restore.cleanupKubernetesRestore(context.Background(), run.get())
	if n, _ := run.replicas(kamajiControlPlaneGVR, k8sTestRelease); n != 0 {
		t.Fatal("deleting the RestoreJob brought the control plane up while the restore Job may be rewriting the prefix")
	}
	job := &batchv1.Job{}
	if err := run.env.client.Get(context.Background(), client.ObjectKey{Namespace: k8sTestNS, Name: "rj-restore"}, job); err == nil {
		t.Fatal("the running restore Job was left to keep writing")
	}
}

func TestReconcileKubernetesRestoreIgnoresModeAnnotation(t *testing.T) {
	run := newK8sRestoreRun(t, "", k8sBackupForRestore())
	rj := run.get()
	rj.Annotations = map[string]string{"kubernetes.strategy.backups.cozystack.io/restore-state": `{"mode":"recreate","release":"kubernetes-c1"}`}
	if err := run.env.client.Update(context.Background(), rj); err != nil {
		t.Fatal(err)
	}
	run.verifyAndCapture(true)
	if state := kubernetesStateFromRestoreJob(run.get()); state == nil || state.Mode != kubernetesRestoreModeInPlace {
		t.Fatalf("a live cluster must be restored in place whatever the RestoreJob claims: %+v", state)
	}
}

func TestReconcileKubernetesBackupDiscardsDumpWhenVeleroFails(t *testing.T) {
	ctx := context.Background()
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: k8sTestNS, Name: "bj2"},
		Spec: backupsv1alpha1.BackupJobSpec{ApplicationRef: corev1.TypedLocalObjectReference{
			APIGroup: new(backupsv1alpha1.DefaultApplicationAPIGroup), Kind: kubernetesAppKind, Name: k8sTestApp,
		}},
	}
	env := newKubernetesTestEnv(t, bj)
	step := func() {
		t.Helper()
		got := &backupsv1alpha1.BackupJob{}
		if err := env.client.Get(ctx, client.ObjectKeyFromObject(bj), got); err != nil {
			t.Fatal(err)
		}
		if _, err := env.backup.reconcileKubernetes(ctx, got, k8sResolved()); err != nil {
			t.Fatal(err)
		}
	}
	step()
	step()
	step()
	dump := &batchv1.Job{}
	if err := env.client.Get(ctx, client.ObjectKey{Namespace: k8sTestNS, Name: "bj2-backup"}, dump); err != nil {
		t.Fatal(err)
	}
	dump.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := env.client.Status().Update(ctx, dump); err != nil {
		t.Fatal(err)
	}
	step()
	vbs := &velerov1.BackupList{}
	if err := env.client.List(ctx, vbs, client.MatchingLabels{backupsv1alpha1.OwningJobNameLabel: "bj2"}); err != nil || len(vbs.Items) != 1 {
		t.Fatalf("Velero Backups: %d %v", len(vbs.Items), err)
	}
	vb := vbs.Items[0]
	vb.Status.Phase = velerov1.BackupPhaseFailed
	if err := env.client.Update(ctx, &vb); err != nil {
		t.Fatal(err)
	}
	step()
	got := &backupsv1alpha1.BackupJob{}
	_ = env.client.Get(ctx, client.ObjectKeyFromObject(bj), got)
	if got.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("BackupJob not failed: %+v", got.Status)
	}
	discard := &batchv1.Job{}
	if err := env.client.Get(ctx, client.ObjectKey{Namespace: k8sTestNS, Name: "bj2-discard"}, discard); err != nil {
		t.Fatalf("the orphaned etcd dump is not scheduled for deletion: %v", err)
	}
	if args := discard.Spec.Template.Spec.Containers[0].Args[0]; !strings.HasPrefix(args, "cleanup tenant-a/c1/bj2.etcd.json") {
		t.Fatalf("discard Job does not delete this run's dump: %q", args)
	}
}

func TestReconcileKubernetesRestoreRecreateFailureBeforeWriteStartsControlPlane(t *testing.T) {
	ctx := context.Background()
	run := newK8sRestoreRun(t, `{"volumes":false}`, k8sBackupOfDeletedCluster())
	run.verifyAndCapture(true)
	run.step() // secrets
	run.finishVeleroRestore(velerov1.RestorePhaseCompleted)
	run.step()
	run.step() // cluster HelmRelease
	dyn := run.env.restore.Interface
	if _, err := dyn.Resource(tenantControlPlaneGVR).Namespace(k8sTestNS).Create(ctx, unstr(schema.GroupVersionKind{Group: "kamaji.clastix.io", Version: "v1alpha1", Kind: "TenantControlPlane"}, k8sTestNS, "kubernetes-gone", map[string]any{
		"status": map[string]any{"storage": map[string]any{"driver": "etcd", "dataStoreName": "tenant-a", "setup": map[string]any{"schema": "uid-1"}}},
	}), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := dyn.Resource(kamajiControlPlaneGVR).Namespace(k8sTestNS).Create(ctx, unstr(schema.GroupVersionKind{Group: "controlplane.cluster.x-k8s.io", Version: "v1alpha1", Kind: "KamajiControlPlane"}, k8sTestNS, "kubernetes-gone", map[string]any{
		"spec": map[string]any{"replicas": int64(2)},
	}), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	run.step() // control plane is up
	run.step() // stopped
	run.step() // etcd Job
	run.addJobPod("rj-restore", "")
	run.finishJob("rj-restore", batchv1.JobFailed)
	run.step()
	if got := run.get(); got.Status.Phase != backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("rebuild not failed: %+v", got.Status)
	}
	if n, _ := run.replicas(kamajiControlPlaneGVR, "kubernetes-gone"); n != 2 {
		t.Fatalf("the rebuilt control plane is left stopped although its prefix was never changed: %d", n)
	}
}

func TestReconcileKubernetesRestoreJobThatNeverRanResumes(t *testing.T) {
	run := newK8sRestoreRun(t, `{"volumes":false}`, k8sBackupForRestore())
	run.verifyAndCapture(true)
	run.step() // stop
	run.step() // etcd Job
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: k8sTestNS, Name: "rj-restore-pod", Labels: map[string]string{"job-name": "rj-restore"}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "etcd",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
		}}},
	}
	if err := run.env.client.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	run.step() // still waiting on the Job
	if meta.IsStatusConditionTrue(run.get().Status.Conditions, kubernetesCondEtcdJobStarted) {
		t.Fatal("a container that never ran was taken for a started Job")
	}
	run.finishJob("rj-restore", batchv1.JobFailed)
	run.step()
	if n, _ := run.replicas(kamajiControlPlaneGVR, k8sTestRelease); n != 2 {
		t.Fatalf("control plane left stopped after a restore Job that never ran: %d", n)
	}
}

func TestReconcileKubernetesRestoreJobPodsGoneAfterStart(t *testing.T) {
	run := newK8sRestoreRun(t, `{"volumes":false}`, k8sBackupForRestore())
	run.verifyAndCapture(true)
	run.step() // stop
	run.step() // etcd Job
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: k8sTestNS, Name: "rj-restore-pod", Labels: map[string]string{"job-name": "rj-restore"}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "etcd",
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}}},
	}
	if err := run.env.client.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	run.step()
	if !meta.IsStatusConditionTrue(run.get().Status.Conditions, kubernetesCondEtcdJobStarted) {
		t.Fatal("a running restore Job was not recorded as started")
	}
	// The Job deadline deletes its Pods and fails it.
	if err := run.env.client.Delete(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	run.finishJob("rj-restore", batchv1.JobFailed)
	run.step()
	if n, _ := run.replicas(kamajiControlPlaneGVR, k8sTestRelease); n != 0 {
		t.Fatal("control plane brought up after a restore Job that ran and left no trace of how far it got")
	}
}

func TestReconcileKubernetesRestoreRetriesStopAfterAPIError(t *testing.T) {
	run := newK8sRestoreRun(t, `{"volumes":false}`, k8sBackupForRestore())
	run.verifyAndCapture(true)
	failed := false
	run.env.restore.Interface.(*dynamicfake.FakeDynamicClient).PrependReactor("patch", "kamajicontrolplanes", func(clienttesting.Action) (bool, runtime.Object, error) {
		if failed {
			return false, nil, nil
		}
		failed = true
		return true, nil, fmt.Errorf("etcdserver: request timed out")
	})
	run.step()
	got := run.get()
	if got.Status.Phase == backupsv1alpha1.RestoreJobPhaseFailed {
		t.Fatalf("one API error aborted the restore: %+v", got.Status)
	}
	if c := meta.FindStatusCondition(got.Status.Conditions, kubernetesCondStopped); c == nil || !strings.Contains(c.Message, "API error") {
		t.Fatalf("the retried API error is not reported: %+v", c)
	}
	run.step()
	if !meta.IsStatusConditionTrue(run.get().Status.Conditions, kubernetesCondStopped) {
		t.Fatal("the stop did not go through once the API answered again")
	}
}

// driveRecreateToEtcdJob runs a rebuild up to the etcd restore Job, the new
// TenantControlPlane reporting schema.
func driveRecreateToEtcdJob(t *testing.T, run *k8sRestoreRun, schemaName string) {
	t.Helper()
	ctx := context.Background()
	run.verifyAndCapture(true)
	run.step() // secrets
	run.finishVeleroRestore(velerov1.RestorePhaseCompleted)
	run.step()
	run.step() // volumes
	run.finishVeleroRestore(velerov1.RestorePhaseCompleted)
	run.step()
	run.step() // cluster HelmRelease
	dyn := run.env.restore.Interface
	if _, err := dyn.Resource(tenantControlPlaneGVR).Namespace(k8sTestNS).Create(ctx, unstr(schema.GroupVersionKind{Group: "kamaji.clastix.io", Version: "v1alpha1", Kind: "TenantControlPlane"}, k8sTestNS, "kubernetes-gone", map[string]any{
		"status": map[string]any{"storage": map[string]any{"driver": "etcd", "dataStoreName": "tenant-a", "setup": map[string]any{"schema": schemaName}}},
	}), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := dyn.Resource(kamajiControlPlaneGVR).Namespace(k8sTestNS).Create(ctx, unstr(schema.GroupVersionKind{Group: "controlplane.cluster.x-k8s.io", Version: "v1alpha1", Kind: "KamajiControlPlane"}, k8sTestNS, "kubernetes-gone", map[string]any{
		"spec": map[string]any{"replicas": int64(2)},
	}), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	run.step() // control plane is up
	run.step() // stopped
	run.step() // etcd Job
}

func TestReconcileKubernetesRestoreRecreateForcesSurvivingPool(t *testing.T) {
	ctx := context.Background()
	run := newK8sRestoreRun(t, "", k8sBackupOfDeletedCluster())
	// Deleting only the Kubernetes application leaves its KubernetesNodes
	// pool's HelmRelease behind, while the pool's MachineDeployment went
	// with the CAPI Cluster.
	pool := unstr(schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"}, k8sTestNS, "kubernetes-nodes-gone-md0", map[string]any{"spec": map[string]any{"interval": "5m"}})
	if _, err := run.env.restore.Interface.Resource(helmReleaseGVR).Namespace(k8sTestNS).Create(ctx, pool, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	driveRecreateToEtcdJob(t, run, "uid-1")
	run.finishJob("rj-restore", batchv1.JobComplete)
	run.step()
	run.step()
	if got := run.get(); got.Status.Phase != backupsv1alpha1.RestoreJobPhaseSucceeded {
		t.Fatalf("rebuild not succeeded: %+v", got.Status)
	}
	ann := run.obj(helmReleaseGVR, "kubernetes-nodes-gone-md0").GetAnnotations()
	if ann["reconcile.fluxcd.io/forceAt"] == "" || ann["reconcile.fluxcd.io/forceAt"] != ann["reconcile.fluxcd.io/requestedAt"] {
		t.Fatalf("the surviving pool is not forced to recreate its MachineDeployment: %v", ann)
	}
}

func TestReconcileKubernetesBackupRefusesClusterStoppedByRestore(t *testing.T) {
	ctx := context.Background()
	bj := &backupsv1alpha1.BackupJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: k8sTestNS, Name: "bj3"},
		Spec: backupsv1alpha1.BackupJobSpec{ApplicationRef: corev1.TypedLocalObjectReference{
			APIGroup: new(backupsv1alpha1.DefaultApplicationAPIGroup), Kind: kubernetesAppKind, Name: k8sTestApp,
		}},
	}
	env := newKubernetesTestEnv(t, bj)
	patch := []byte(`{"metadata":{"annotations":{"` + kubernetesReplicasBeforeAnnotation + `":"2"}},"spec":{"replicas":0}}`)
	if _, err := env.backup.Interface.Resource(kamajiControlPlaneGVR).Namespace(k8sTestNS).Patch(ctx, k8sTestRelease, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		got := &backupsv1alpha1.BackupJob{}
		_ = env.client.Get(ctx, client.ObjectKeyFromObject(bj), got)
		if _, err := env.backup.reconcileKubernetes(ctx, got, k8sResolved()); err != nil {
			t.Fatal(err)
		}
	}
	got := &backupsv1alpha1.BackupJob{}
	_ = env.client.Get(ctx, client.ObjectKeyFromObject(bj), got)
	if got.Status.Phase != backupsv1alpha1.BackupJobPhaseFailed {
		t.Fatalf("a cluster stopped by an unfinished restore was backed up: %+v", got.Status)
	}
	if err := env.client.Get(ctx, client.ObjectKey{Namespace: k8sTestNS, Name: "bj3-backup"}, &batchv1.Job{}); err == nil {
		t.Fatal("the etcd prefix of a stopped cluster was dumped")
	}
}

func TestStopKubernetesClusterWaitsForTerminatingControlPlane(t *testing.T) {
	ctx := context.Background()
	run := newK8sRestoreRun(t, `{"volumes":false}`, k8sBackupForRestore())
	cp := unstr(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, k8sTestNS, k8sTestRelease, map[string]any{
		"spec":   map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"kamaji.clastix.io/name": k8sTestRelease}}},
		"status": map[string]any{"replicas": int64(0)},
	})
	if _, err := run.env.restore.Interface.Resource(deploymentGVR).Namespace(k8sTestNS).Create(ctx, cp, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: k8sTestNS, Name: "apiserver-0", Labels: map[string]string{"kamaji.clastix.io/name": k8sTestRelease}}}
	if err := run.env.client.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	run.verifyAndCapture(true)
	run.step()
	if meta.IsStatusConditionTrue(run.get().Status.Conditions, kubernetesCondStopped) {
		t.Fatal("cluster reported stopped while a control-plane Pod is still terminating")
	}
	if err := run.env.client.Delete(ctx, pod); err != nil {
		t.Fatal(err)
	}
	run.step()
	if !meta.IsStatusConditionTrue(run.get().Status.Conditions, kubernetesCondStopped) {
		t.Fatal("cluster not reported stopped once its control plane was gone")
	}
}

func TestKubernetesJobReachedWriteConservativeBranches(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status corev1.ContainerStatus
		want   bool
	}{
		{"running", corev1.ContainerStatus{Name: "etcd", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}, true},
		{"restarted after writing", corev1.ContainerStatus{Name: "etcd",
			State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: kubernetesJobWriting}}}, true},
		{"never ran", corev1.ContainerStatus{Name: "etcd", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}, false},
		{"failed before writing", corev1.ContainerStatus{Name: "etcd", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := newK8sRestoreRun(t, "", k8sBackupForRestore())
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Namespace: k8sTestNS, Name: "p", Labels: map[string]string{"job-name": "rj-restore"}},
				Status:     corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{tc.status}},
			}
			if err := run.env.client.Create(context.Background(), pod); err != nil {
				t.Fatal(err)
			}
			got, err := run.env.restore.kubernetesJobReachedWrite(context.Background(), run.get(), "rj-restore")
			if err != nil || got != tc.want {
				t.Fatalf("reached write = %v (%v), want %v", got, err, tc.want)
			}
		})
	}
}
