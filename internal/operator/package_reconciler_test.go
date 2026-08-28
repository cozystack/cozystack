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
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/apis/kustomize"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/yaml"
)

func TestParseCRDPolicy(t *testing.T) {
	tests := []struct {
		name    string
		install *cozyv1alpha1.ComponentInstall
		want    helmv2.CRDsPolicy
	}{
		{
			name:    "nil install leaves flux default",
			install: nil,
			want:    "",
		},
		{
			name:    "empty upgradeCRDs leaves flux default",
			install: &cozyv1alpha1.ComponentInstall{},
			want:    "",
		},
		{
			name:    "Skip is passed through",
			install: &cozyv1alpha1.ComponentInstall{UpgradeCRDs: "Skip"},
			want:    helmv2.Skip,
		},
		{
			name:    "Create is passed through",
			install: &cozyv1alpha1.ComponentInstall{UpgradeCRDs: "Create"},
			want:    helmv2.Create,
		},
		{
			name:    "CreateReplace is passed through",
			install: &cozyv1alpha1.ComponentInstall{UpgradeCRDs: "CreateReplace"},
			want:    helmv2.CreateReplace,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseCRDPolicy(tc.install)
			if got != tc.want {
				t.Errorf("parseCRDPolicy() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildHelmReleaseSpec(t *testing.T) {
	r := &PackageReconciler{
		HelmReleaseInterval:       42 * time.Second,
		HelmReleaseRetryInterval:  17 * time.Second,
		HelmReleaseInstallTimeout: 11 * time.Minute,
		HelmReleaseUpgradeTimeout: 13 * time.Minute,
		HelmReleaseMaxHistory:     7,
	}
	componentInstall := &cozyv1alpha1.ComponentInstall{UpgradeCRDs: "Skip"}

	spec := r.buildHelmReleaseSpec(componentInstall, "ps-variant-component")

	if spec.Interval.Duration != 42*time.Second {
		t.Errorf("Interval = %v, want 42s", spec.Interval.Duration)
	}
	if spec.MaxHistory == nil {
		t.Fatal("MaxHistory is nil, want pointer to 7")
	}
	if *spec.MaxHistory != 7 {
		t.Errorf("MaxHistory = %d, want 7", *spec.MaxHistory)
	}

	if spec.ChartRef == nil {
		t.Fatal("ChartRef is nil")
	}
	if spec.ChartRef.Kind != "ExternalArtifact" {
		t.Errorf("ChartRef.Kind = %q, want ExternalArtifact", spec.ChartRef.Kind)
	}
	if spec.ChartRef.Name != "ps-variant-component" {
		t.Errorf("ChartRef.Name = %q, want ps-variant-component", spec.ChartRef.Name)
	}
	if spec.ChartRef.Namespace != "cozy-system" {
		t.Errorf("ChartRef.Namespace = %q, want cozy-system", spec.ChartRef.Namespace)
	}

	if spec.Install == nil {
		t.Fatal("Install is nil")
	}
	if spec.Install.Timeout == nil || spec.Install.Timeout.Duration != 11*time.Minute {
		t.Errorf("Install.Timeout = %v, want 11m", spec.Install.Timeout)
	}
	if spec.Install.Strategy == nil {
		t.Fatal("Install.Strategy is nil")
	}
	if spec.Install.Strategy.Name != string(helmv2.ActionStrategyRetryOnFailure) {
		t.Errorf("Install.Strategy.Name = %q, want %q", spec.Install.Strategy.Name, helmv2.ActionStrategyRetryOnFailure)
	}
	if spec.Install.Strategy.RetryInterval == nil || spec.Install.Strategy.RetryInterval.Duration != 17*time.Second {
		t.Errorf("Install.Strategy.RetryInterval = %v, want 17s", spec.Install.Strategy.RetryInterval)
	}
	// Remediation must remain nil: retries are driven solely by
	// Strategy.Name=RetryOnFailure with an explicit RetryInterval.
	// Re-introducing Remediation{Retries: -1} "for safety" would add a
	// second, conflicting retry mechanism alongside the strategy retry
	// path.
	if spec.Install.Remediation != nil {
		t.Errorf("Install.Remediation = %+v, want nil", spec.Install.Remediation)
	}

	if spec.Upgrade == nil {
		t.Fatal("Upgrade is nil")
	}
	if spec.Upgrade.Timeout == nil || spec.Upgrade.Timeout.Duration != 13*time.Minute {
		t.Errorf("Upgrade.Timeout = %v, want 13m", spec.Upgrade.Timeout)
	}
	if spec.Upgrade.Strategy == nil {
		t.Fatal("Upgrade.Strategy is nil")
	}
	if spec.Upgrade.Strategy.Name != string(helmv2.ActionStrategyRetryOnFailure) {
		t.Errorf("Upgrade.Strategy.Name = %q, want %q", spec.Upgrade.Strategy.Name, helmv2.ActionStrategyRetryOnFailure)
	}
	if spec.Upgrade.Strategy.RetryInterval == nil || spec.Upgrade.Strategy.RetryInterval.Duration != 17*time.Second {
		t.Errorf("Upgrade.Strategy.RetryInterval = %v, want 17s", spec.Upgrade.Strategy.RetryInterval)
	}
	if spec.Upgrade.Remediation != nil {
		t.Errorf("Upgrade.Remediation = %+v, want nil", spec.Upgrade.Remediation)
	}
	if spec.Upgrade.CRDs != helmv2.Skip {
		t.Errorf("Upgrade.CRDs = %q, want Skip", spec.Upgrade.CRDs)
	}
}

// TestBuildHelmReleaseSpecZeroMaxHistory pins that MaxHistory=0 (unlimited
// history per Helm semantics) survives the spec build — i.e. is set as a
// non-nil pointer to 0 rather than dropped or replaced with a default.
// buildHelmReleaseSpec threads the kstatus readiness knobs from ComponentInstall
// (issue #2642) onto the generated HelmRelease, coupling the poller default when
// healthCheckExprs are present but no wait strategy was set.
func TestBuildHelmReleaseSpecHealthCheckExprs(t *testing.T) {
	r := &PackageReconciler{HelmReleaseMaxHistory: 7}
	exprs := []kustomize.CustomHealthCheck{{
		APIVersion: "postgresql.cnpg.io/v1",
		Kind:       "Cluster",
	}}

	// exprs present, no explicit strategy -> poller (coupled default)
	spec := r.buildHelmReleaseSpec(&cozyv1alpha1.ComponentInstall{HealthCheckExprs: exprs}, "x")
	if len(spec.HealthCheckExprs) != 1 || spec.HealthCheckExprs[0].Kind != "Cluster" {
		t.Fatalf("HealthCheckExprs not threaded: %+v", spec.HealthCheckExprs)
	}
	if spec.WaitStrategy == nil || spec.WaitStrategy.Name != helmv2.WaitStrategyPoller {
		t.Errorf("WaitStrategy = %+v, want poller (coupled default)", spec.WaitStrategy)
	}

	// explicit legacy honored even with exprs
	spec = r.buildHelmReleaseSpec(&cozyv1alpha1.ComponentInstall{HealthCheckExprs: exprs, WaitStrategy: "legacy"}, "x")
	if spec.WaitStrategy == nil || spec.WaitStrategy.Name != helmv2.WaitStrategyLegacy {
		t.Errorf("WaitStrategy = %+v, want legacy", spec.WaitStrategy)
	}

	// no exprs, no strategy -> unset (flux default)
	spec = r.buildHelmReleaseSpec(&cozyv1alpha1.ComponentInstall{}, "x")
	if spec.WaitStrategy != nil {
		t.Errorf("WaitStrategy = %+v, want nil", spec.WaitStrategy)
	}
	if spec.HealthCheckExprs != nil {
		t.Errorf("HealthCheckExprs = %+v, want nil", spec.HealthCheckExprs)
	}
}

func TestBuildHelmReleaseSpecZeroMaxHistory(t *testing.T) {
	r := &PackageReconciler{HelmReleaseMaxHistory: 0}
	spec := r.buildHelmReleaseSpec(nil, "x")
	if spec.MaxHistory == nil {
		t.Fatal("MaxHistory is nil for HelmReleaseMaxHistory=0; want pointer to 0")
	}
	if *spec.MaxHistory != 0 {
		t.Errorf("MaxHistory = %d, want 0", *spec.MaxHistory)
	}
}

// TestPackageSourceCRDHasUpgradeCRDsEnum guards the generated CRD schema: the
// invalid-value case from the spec is enforced at the API server via a
// kubebuilder enum marker, not in the reconciler. If someone drops the marker
// and forgets to regenerate, this test catches it.
func TestPackageSourceCRDHasUpgradeCRDsEnum(t *testing.T) {
	path := filepath.Join("..", "crdinstall", "manifests", "cozystack.io_packagesources.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(data, &crd); err != nil {
		t.Fatalf("unmarshal CRD: %v", err)
	}

	var field *apiextensionsv1.JSONSchemaProps
	for i := range crd.Spec.Versions {
		v := &crd.Spec.Versions[i]
		if v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
			continue
		}
		spec, ok := v.Schema.OpenAPIV3Schema.Properties["spec"]
		if !ok {
			continue
		}
		variants, ok := spec.Properties["variants"]
		if !ok || variants.Items == nil || variants.Items.Schema == nil {
			continue
		}
		components, ok := variants.Items.Schema.Properties["components"]
		if !ok || components.Items == nil || components.Items.Schema == nil {
			continue
		}
		install, ok := components.Items.Schema.Properties["install"]
		if !ok {
			continue
		}
		f, ok := install.Properties["upgradeCRDs"]
		if !ok {
			continue
		}
		field = &f
		break
	}

	if field == nil {
		t.Fatal("upgradeCRDs field not found in PackageSource CRD schema")
	}

	got := map[string]bool{}
	for _, e := range field.Enum {
		var s string
		if err := json.Unmarshal(e.Raw, &s); err != nil {
			t.Fatalf("unmarshal enum value %q: %v", e.Raw, err)
		}
		got[s] = true
	}

	for _, want := range []string{"Skip", "Create", "CreateReplace"} {
		if !got[want] {
			t.Errorf("enum value %q missing from upgradeCRDs; got %v", want, got)
		}
	}
}

func TestSystemDefaultsLimitRange(t *testing.T) {
	r := &PackageReconciler{
		SystemNamespaceMemoryLimit:   resource.MustParse("4Gi"),
		SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
	}

	lr := r.systemDefaultsLimitRange("cozy-metallb", r.SystemNamespaceMemoryLimit)

	if lr.Name != SystemDefaultsLimitRangeName {
		t.Errorf("name = %q, want %q", lr.Name, SystemDefaultsLimitRangeName)
	}
	if lr.Namespace != "cozy-metallb" {
		t.Errorf("namespace = %q, want cozy-metallb", lr.Namespace)
	}
	if len(lr.Spec.Limits) != 1 {
		t.Fatalf("len(limits) = %d, want 1", len(lr.Spec.Limits))
	}

	item := lr.Spec.Limits[0]
	if item.Type != corev1.LimitTypeContainer {
		t.Errorf("type = %q, want Container", item.Type)
	}

	// The default limit is what puts memory.max on the pod cgroup, which is the only
	// thing that removes a pod from the Talos OOM handler's victim set.
	if got := item.Default[corev1.ResourceMemory]; got.Cmp(resource.MustParse("4Gi")) != 0 {
		t.Errorf("default memory = %s, want 4Gi", got.String())
	}
	// defaultRequest must be present and small. If it were omitted, Kubernetes would
	// default each request to the limit and reserve 4Gi per system container.
	got := item.DefaultRequest[corev1.ResourceMemory]
	if got.Cmp(resource.MustParse("32Mi")) != 0 {
		t.Errorf("defaultRequest memory = %s, want 32Mi", got.String())
	}
	if wantMax := item.Default[corev1.ResourceMemory]; got.Cmp(wantMax) > 0 {
		t.Errorf("defaultRequest %s exceeds default %s; the API server rejects such a LimitRange",
			got.String(), wantMax.String())
	}

	// Only memory is defaulted. A default CPU limit would throttle system components.
	if _, ok := item.Default[corev1.ResourceCPU]; ok {
		t.Error("default sets a CPU limit; only memory should be defaulted")
	}
	if _, ok := item.DefaultRequest[corev1.ResourceCPU]; ok {
		t.Error("defaultRequest sets a CPU request; only memory should be defaulted")
	}
}

func TestReconcileSystemDefaultsLimitRangeDisabledRemovesStale(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}

	// Built through the same constructor the operator applies with, so the object
	// carries the managed-by label the disabled path keys on.
	existing := (&PackageReconciler{}).systemDefaultsLimitRange("cozy-metallb", resource.MustParse("4Gi"))

	var deletes int
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				deletes++
				return c.Delete(ctx, obj, opts...)
			},
		}).Build()

	// Zero limit means the knob is disabled; a LimitRange from an earlier
	// configuration must be removed so the setting is reversible.
	r := &PackageReconciler{Client: cl, Scheme: scheme}

	if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-metallb"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	err := cl.Get(t.Context(), types.NamespacedName{
		Name:      SystemDefaultsLimitRangeName,
		Namespace: "cozy-metallb",
	}, &corev1.LimitRange{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("LimitRange still present after disable (err = %v)", err)
	}
	if deletes != 1 {
		t.Fatalf("%d deletes to remove one LimitRange, want 1", deletes)
	}

	// Disabled is a steady state: this runs for every system namespace on every
	// Package reconcile. Reconciling again with nothing to delete must stay a no-op
	// and must not issue a DELETE whose 404 is then swallowed — that is one write
	// attempt per namespace per reconcile, forever, and all of it in the audit log.
	if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-metallb"); err != nil {
		t.Fatalf("reconcile on absent LimitRange: %v", err)
	}
	if deletes != 1 {
		t.Errorf("%d deletes after reconciling an already-empty namespace, want the original 1; "+
			"the disabled path must read before it writes", deletes)
	}
}

// The name is not reserved. Nothing stops an administrator creating a LimitRange called
// cozystack-system-defaults by hand, and deleting by name alone would have turning the knob
// off silently remove policy this operator never wrote. The managed-by label is what
// separates the two, and it is also the answer if the name is ever taken over by another
// component.
func TestReconcileSystemDefaultsLimitRangeDisabledLeavesAForeignObjectAlone(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}

	foreign := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SystemDefaultsLimitRangeName,
			Namespace: "cozy-metallb",
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "some-admin"},
		},
		Spec: corev1.LimitRangeSpec{
			Limits: []corev1.LimitRangeItem{{
				Type:    corev1.LimitTypeContainer,
				Default: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Mi")},
			}},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(foreign).Build()

	r := &PackageReconciler{Client: cl, Scheme: scheme}

	if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-metallb"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := &corev1.LimitRange{}
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name:      SystemDefaultsLimitRangeName,
		Namespace: "cozy-metallb",
	}, got); err != nil {
		t.Fatalf("a LimitRange this operator did not create was deleted when the knob was disabled: %v", err)
	}
	if len(got.Spec.Limits) != 1 || got.Spec.Limits[0].Default.Memory().Cmp(resource.MustParse("1Mi")) != 0 {
		t.Errorf("foreign LimitRange spec = %+v, want it untouched", got.Spec.Limits)
	}
}

// limitRangeScheme builds the scheme the LimitRange reconcile tests share. The scan reads
// workload pod templates as well as pods, so apps/v1 and batch/v1 have to be registered —
// the operator gets them from clientgoscheme.AddToScheme in main.go.
func limitRangeScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add appsv1 to scheme: %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add batchv1 to scheme: %v", err)
	}
	return scheme
}

// systemPodSpec builds the pod spec every fixture in these tests shares: one container
// carrying the given memory request and, when limit is non-empty, its own memory limit.
//
// The container is named after the object that carries it, so a test asserting which
// container was reported cannot pass by accident when every fixture carries an
// identically named container.
func systemPodSpec(name, request, limit string) corev1.PodSpec {
	requirements := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(request)},
	}
	if limit != "" {
		requirements.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(limit)}
	}
	return corev1.PodSpec{
		Containers: []corev1.Container{{Name: name + "-app", Resources: requirements}},
	}
}

// systemPod builds a running pod with a single container carrying the given memory request
// and, when limit is non-empty, its own memory limit.
func systemPod(name, request, limit string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cozy-monitoring"},
		Spec:       systemPodSpec(name, request, limit),
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

// systemDeployment builds a Deployment whose pod template carries the given request.
// replicas is set for real rather than left nil, so a case named for a scaled-to-zero
// workload is actually scaled to zero.
func systemDeployment(name string, replicas int32, request, limit string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cozy-monitoring"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       systemPodSpec(name, request, limit),
			},
		},
	}
}

// systemStatefulSet builds a StatefulSet whose pod template carries the given request.
func systemStatefulSet(name string, replicas int32, request, limit string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cozy-monitoring"},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       systemPodSpec(name, request, limit),
			},
		},
	}
}

// systemDaemonSet builds a DaemonSet whose pod template carries the given request.
func systemDaemonSet(name, request, limit string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cozy-monitoring"},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       systemPodSpec(name, request, limit),
			},
		},
	}
}

// systemCronJob builds a CronJob whose job template carries the given request. A CronJob
// between runs has no pods at all, which is the state the pod scan cannot see.
func systemCronJob(name, request, limit string) *batchv1.CronJob {
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cozy-monitoring"},
		Spec: batchv1.CronJobSpec{
			Schedule: "0 * * * *",
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{Spec: systemPodSpec(name, request, limit)},
				},
			},
		},
	}
}

// systemJob builds a Job whose pod template carries the given request.
func systemJob(name, request, limit string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cozy-monitoring"},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{Spec: systemPodSpec(name, request, limit)},
		},
	}
}

func limitRangeExists(t *testing.T, cl client.Client, ns string) bool {
	t.Helper()
	err := cl.Get(t.Context(), types.NamespacedName{
		Name:      SystemDefaultsLimitRangeName,
		Namespace: ns,
	}, &corev1.LimitRange{})
	if err == nil {
		return true
	}
	if apierrors.IsNotFound(err) {
		return false
	}
	t.Fatalf("get LimitRange: %v", err)
	return false
}

// limitRangeDefaultMemory returns the default container memory limit the operator has
// applied in ns.
func limitRangeDefaultMemory(t *testing.T, cl client.Client, ns string) resource.Quantity {
	t.Helper()
	lr := &corev1.LimitRange{}
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name:      SystemDefaultsLimitRangeName,
		Namespace: ns,
	}, lr); err != nil {
		t.Fatalf("get LimitRange in %s: %v", ns, err)
	}
	if len(lr.Spec.Limits) != 1 {
		t.Fatalf("len(limits) = %d, want 1", len(lr.Spec.Limits))
	}
	return lr.Spec.Limits[0].Default[corev1.ResourceMemory]
}

// The mirror case, and the one that keeps the guard from being a blanket opt-out:
// LimitRanger never defaults a container that already declares a memory limit, so a
// large request paired with its own limit cannot be broken by the LimitRange and must
// not stop the rest of the namespace being protected.
func TestReconcileSystemDefaultsLimitRangeAppliesWhenLargeRequestCarriesItsOwnLimit(t *testing.T) {
	scheme := limitRangeScheme(t)

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(systemPod("vmstorage-0", "8Gi", "8Gi")).Build()

	r := &PackageReconciler{
		Client:                       cl,
		APIReader:                    cl,
		Scheme:                       scheme,
		SystemNamespaceMemoryLimit:   resource.MustParse("4Gi"),
		SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
	}

	if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-monitoring"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if !limitRangeExists(t, cl, "cozy-monitoring") {
		t.Fatal("LimitRange withheld from a namespace whose only large request declares its own limit")
	}
}

// A namespace whose pods cannot be read is a namespace where neither applying nor
// retracting is a decision the operator is entitled to make, so it must do neither —
// and it must not report failure either, or a transient API error would fail the whole
// Package reconcile.
// A read the scan needs and cannot get is not evidence that applying is safe: every list
// the scan makes has to fail closed, or the guard becomes a coin toss the first time one
// kind is unreadable. Each kind gets its own case, because a single unreadable kind is the
// realistic failure — an RBAC rule that was never extended, a CRD-less API surface, one
// request that times out — and a scan that swallowed the error for that one kind would
// still look green on every other.
func TestReconcileSystemDefaultsLimitRangeSkipsWhenTheScanCannotRead(t *testing.T) {
	scheme := limitRangeScheme(t)

	// Every one of the six lists is read on every reconcile now that the pod half is no
	// longer skippable, so every case is seeded the same way and asserts the same thing.
	unreadable := []struct {
		name string
		list client.ObjectList
	}{
		{name: "pods", list: &corev1.PodList{}},
		{name: "deployments", list: &appsv1.DeploymentList{}},
		{name: "statefulsets", list: &appsv1.StatefulSetList{}},
		{name: "daemonsets", list: &appsv1.DaemonSetList{}},
		{name: "cronjobs", list: &batchv1.CronJobList{}},
		{name: "jobs", list: &batchv1.JobList{}},
	}

	// A sentinel the operator's own apply would overwrite. Asserting the LimitRange still
	// exists is not enough on its own: a scan that swallowed the read error would find no
	// offender, apply the LimitRange, and leave an object that exists just the same. The
	// sentinel is what separates "left alone" from "applied blind".
	sentinel := resource.MustParse("1Mi")

	for _, tt := range unreadable {
		t.Run(tt.name, func(t *testing.T) {
			seed := []client.Object{
				&corev1.LimitRange{
					ObjectMeta: metav1.ObjectMeta{
						Name:      SystemDefaultsLimitRangeName,
						Namespace: "cozy-monitoring",
					},
					Spec: corev1.LimitRangeSpec{
						Limits: []corev1.LimitRangeItem{{
							Type:    corev1.LimitTypeContainer,
							Default: corev1.ResourceList{corev1.ResourceMemory: sentinel},
						}},
					},
				},
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(seed...).
				WithInterceptorFuncs(interceptor.Funcs{
					List: func(_ context.Context, _ client.WithWatch, list client.ObjectList, _ ...client.ListOption) error {
						if reflect.TypeOf(list) == reflect.TypeOf(tt.list) {
							return apierrors.NewServiceUnavailable("etcd leader changed")
						}
						return nil
					},
				}).Build()

			r := &PackageReconciler{
				Client:                       cl,
				APIReader:                    cl,
				Scheme:                       scheme,
				SystemNamespaceMemoryLimit:   resource.MustParse("4Gi"),
				SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
			}

			if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-monitoring"); err != nil {
				t.Fatalf("an unreadable %s list must not fail the reconcile: %v", tt.name, err)
			}

			got := &corev1.LimitRange{}
			err := cl.Get(t.Context(), types.NamespacedName{
				Name:      SystemDefaultsLimitRangeName,
				Namespace: "cozy-monitoring",
			}, got)

			if err != nil {
				t.Fatalf("existing LimitRange retracted on the strength of a failed %s read: %v", tt.name, err)
			}
			if len(got.Spec.Limits) != 1 || got.Spec.Limits[0].Default.Memory().Cmp(sentinel) != 0 {
				t.Errorf("LimitRange applied on the strength of a failed %s read: spec = %+v, want the untouched sentinel %s",
					tt.name, got.Spec.Limits, sentinel.String())
			}
		})
	}
}

// scanRecordingClient builds a fake client that records which kinds a reconcile listed and
// counts the writes it made, so a test can prove which half of the scan ran rather than
// merely that it reached the same answer.
func scanRecordingClient(scheme *runtime.Scheme, listed *[]string, writes *int, objects ...client.Object) client.WithWatch {
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				*listed = append(*listed, fmt.Sprintf("%T", list))
				return c.List(ctx, list, opts...)
			},
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				*writes++
				return c.Patch(ctx, obj, patch, opts...)
			},
			Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
				*writes++
				return c.Apply(ctx, obj, opts...)
			},
		}).Build()
}

func listedKind(listed []string, kind string) bool {
	for _, l := range listed {
		if strings.Contains(l, kind) {
			return true
		}
	}
	return false
}

// The comparison guarding that write has to be semantic rather than structural, or a
// LimitRange written in a different but equivalent notation would re-apply on every
// reconcile — invisible in a test that only looked at the resulting object, since every one
// of those applies produces the same object.
//
// The fixture writes the same two quantities in plain bytes: 4294967296 is 4Gi and 33554432
// is 32Mi, but they parse as DecimalSI where the configured values are BinarySI, so the
// quantities differ in both format and cached string while comparing equal. Suffixed
// notation would not do: an API round trip canonicalises 4096Mi to 4Gi, which is
// structurally identical to the configured value and would pin nothing.
func TestReconcileSystemDefaultsLimitRangeDoesNotReapplyEquivalentQuantities(t *testing.T) {
	scheme := limitRangeScheme(t)

	// Carries the managed-by label, because this fixture stands for an object the
	// reconciler wrote itself; without it the case would be re-applied over the missing
	// label and stop saying anything about how quantities compare.
	settled := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SystemDefaultsLimitRangeName,
			Namespace: "cozy-monitoring",
			Labels:    map[string]string{managedByLabel: packageControllerFieldOwner},
		},
		Spec: corev1.LimitRangeSpec{
			Limits: []corev1.LimitRangeItem{{
				Type:           corev1.LimitTypeContainer,
				Default:        corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("4294967296")},
				DefaultRequest: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("33554432")},
			}},
		},
	}

	var listed []string
	var writes int
	cl := scanRecordingClient(scheme, &listed, &writes, settled)

	r := &PackageReconciler{
		Client:                       cl,
		APIReader:                    cl,
		Scheme:                       scheme,
		SystemNamespaceMemoryLimit:   resource.MustParse("4Gi"),
		SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
	}

	if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-monitoring"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if writes != 0 {
		t.Errorf("%d writes against 4294967296/33554432, which is the configured 4Gi/32Mi in plain bytes; "+
			"the spec comparison must be semantic, not structural", writes)
	}
}

// ForceOwnership is safe only after the read proved that the object at this key carries our
// ownership label. The absent case stays non-force so a concurrent foreign create turns into
// an SSA conflict instead of having its atomic LimitRangeSpec.Limits list overwritten.
func TestReconcileSystemDefaultsLimitRangeForcesOnlyAnOwnedObject(t *testing.T) {
	scheme := limitRangeScheme(t)

	for _, tt := range []struct {
		name      string
		seed      client.Object
		wantForce bool
	}{
		{name: "create path is non-force"},
		{
			name: "owned update is forced",
			seed: (&PackageReconciler{
				SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
			}).systemDefaultsLimitRange("cozy-monitoring", resource.MustParse("4Gi")),
			wantForce: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.seed != nil {
				builder = builder.WithObjects(tt.seed)
			}

			var applyCalls int
			var gotOptions client.ApplyOptions
			cl := builder.WithInterceptorFuncs(interceptor.Funcs{
				Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
					applyCalls++
					gotOptions.ApplyOptions(opts)
					return c.Apply(ctx, obj, opts...)
				},
			}).Build()

			r := &PackageReconciler{
				Client:                       cl,
				APIReader:                    cl,
				Scheme:                       scheme,
				SystemNamespaceMemoryLimit:   resource.MustParse("32Gi"),
				SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
			}
			if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-monitoring"); err != nil {
				t.Fatalf("reconcile: %v", err)
			}

			if applyCalls != 1 {
				t.Fatalf("Apply calls = %d, want 1", applyCalls)
			}
			gotForce := gotOptions.Force != nil && *gotOptions.Force
			if gotForce != tt.wantForce {
				t.Errorf("ForceOwnership = %v, want %v", gotForce, tt.wantForce)
			}
			if gotOptions.FieldManager != packageControllerFieldOwner {
				t.Errorf("field manager = %q, want %q", gotOptions.FieldManager, packageControllerFieldOwner)
			}
		})
	}
}

// The tenant boundary. It is one strings.HasPrefix, and everything downstream of it depends
// on it holding: packages/apps/tenant ships tenant-range-limits, which defaults container
// memory to 128Mi in every tenant namespace with resourceQuotas. A system LimitRange landing
// beside it would leave two LimitRanges defaulting the same containers, and which one wins
// is decided by the order LimitRanger happens to iterate them — neither chart controls that,
// so a tenant container's default memory limit would move between 128Mi and 4Gi with nothing
// in the tree changing.
//
// The system namespace in the same run is the positive control: without it the test would
// still pass if LimitRanges stopped being written anywhere at all.
func TestReconcileNamespacesLeavesTenantNamespacesAlone(t *testing.T) {
	scheme := limitRangeScheme(t)
	if err := cozyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cozyv1alpha1 to scheme: %v", err)
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &PackageReconciler{
		Client:                       cl,
		APIReader:                    cl,
		Scheme:                       scheme,
		SystemNamespaceMemoryLimit:   resource.MustParse("4Gi"),
		SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
	}

	pkg := &cozyv1alpha1.Package{ObjectMeta: metav1.ObjectMeta{Name: "platform"}}
	variant := &cozyv1alpha1.Variant{
		Name: "default",
		Components: []cozyv1alpha1.Component{
			{Name: "metallb", Install: &cozyv1alpha1.ComponentInstall{Namespace: "cozy-metallb"}},
			{Name: "tenant", Install: &cozyv1alpha1.ComponentInstall{Namespace: "tenant-root"}},
		},
	}

	if err := r.reconcileNamespaces(t.Context(), pkg, variant); err != nil {
		t.Fatalf("reconcileNamespaces: %v", err)
	}

	if limitRangeExists(t, cl, "tenant-root") {
		t.Error("system defaults LimitRange written into a tenant namespace; it would compete with " +
			"the tenant chart's tenant-range-limits for the same containers")
	}
	if !limitRangeExists(t, cl, "cozy-metallb") {
		t.Fatal("no LimitRange in the system namespace either, so the tenant assertion above proves nothing")
	}

	tenantNS := &corev1.Namespace{}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: "tenant-root"}, tenantNS); err != nil {
		t.Fatalf("tenant namespace was not created: %v", err)
	}
	if got, ok := tenantNS.Labels["cozystack.io/system"]; ok {
		t.Errorf("tenant namespace carries cozystack.io/system = %q; the same boundary gates both", got)
	}
}

// Disable is global even though namespace reconciliation starts from one Package's target
// set. A stale managed object in a namespace that left that set must be swept, while the
// managed-by label keeps a same-named administrator object outside the delete boundary.
func TestReconcileNamespacesDisabledSweepsManagedLimitRanges(t *testing.T) {
	scheme := limitRangeScheme(t)
	if err := cozyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cozyv1alpha1 to scheme: %v", err)
	}

	builder := &PackageReconciler{}
	active := builder.systemDefaultsLimitRange("cozy-active", resource.MustParse("4Gi"))
	orphaned := builder.systemDefaultsLimitRange("cozy-orphaned", resource.MustParse("4Gi"))
	foreign := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SystemDefaultsLimitRangeName,
			Namespace: "cozy-foreign",
			Labels:    map[string]string{managedByLabel: "some-admin"},
		},
	}

	var deletes int
	var perNamespaceGets int
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(active, orphaned, foreign).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.LimitRange); ok {
					perNamespaceGets++
				}
				return c.Get(ctx, key, obj, opts...)
			},
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				deletes++
				return c.Delete(ctx, obj, opts...)
			},
		}).Build()

	r := &PackageReconciler{Client: cl, APIReader: cl, Scheme: scheme}
	pkg := &cozyv1alpha1.Package{ObjectMeta: metav1.ObjectMeta{Name: "platform"}}
	variant := &cozyv1alpha1.Variant{
		Name: "default",
		Components: []cozyv1alpha1.Component{{
			Name:    "active",
			Install: &cozyv1alpha1.ComponentInstall{Namespace: "cozy-active"},
		}},
	}

	if err := r.reconcileNamespaces(t.Context(), pkg, variant); err != nil {
		t.Fatalf("reconcileNamespaces: %v", err)
	}
	if perNamespaceGets != 0 {
		t.Errorf("disabled reconcile made %d per-namespace LimitRange Gets after the global sweep, want 0", perNamespaceGets)
	}
	if deletes != 2 {
		t.Errorf("deleted %d LimitRanges, want the 2 managed objects", deletes)
	}
	if limitRangeExists(t, cl, "cozy-active") {
		t.Error("managed LimitRange in the active target namespace survived disable")
	}
	if limitRangeExists(t, cl, "cozy-orphaned") {
		t.Error("managed LimitRange outside the active target set survived disable")
	}
	if !limitRangeExists(t, cl, "cozy-foreign") {
		t.Error("same-named LimitRange without the operator's ownership label was deleted")
	}
}

// One failed deletion must not stop the sweep before it reaches the remaining namespaces.
// The helper returns the aggregate so the caller can log it, while still removing every
// object the API server lets it remove on this pass.
func TestDeleteManagedSystemDefaultsLimitRangesContinuesAfterDeleteFailure(t *testing.T) {
	scheme := limitRangeScheme(t)
	builder := &PackageReconciler{}
	first := builder.systemDefaultsLimitRange("cozy-first", resource.MustParse("4Gi"))
	second := builder.systemDefaultsLimitRange("cozy-second", resource.MustParse("4Gi"))

	var deleteCalls int
	var failedKey, deletedKey types.NamespacedName
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(first, second).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				deleteCalls++
				key := client.ObjectKeyFromObject(obj)
				if deleteCalls == 1 {
					failedKey = key
					return apierrors.NewForbidden(corev1.Resource("limitranges"), obj.GetName(), fmt.Errorf("denied by policy"))
				}
				deletedKey = key
				return c.Delete(ctx, obj, opts...)
			},
		}).Build()

	r := &PackageReconciler{Client: cl, APIReader: cl, Scheme: scheme}
	err := r.deleteManagedSystemDefaultsLimitRanges(t.Context())
	if err == nil {
		t.Fatal("sweep returned nil after a managed LimitRange deletion failed")
	}
	if deleteCalls != 2 {
		t.Fatalf("sweep attempted %d deletes, want 2; a failed delete must not stop the loop", deleteCalls)
	}
	if failedKey.Name == "" || !strings.Contains(err.Error(), failedKey.Namespace+"/"+failedKey.Name) {
		t.Errorf("error %q does not identify the LimitRange whose deletion failed", err)
	}
	if err := cl.Get(t.Context(), failedKey, &corev1.LimitRange{}); err != nil {
		t.Fatalf("the object whose deletion failed disappeared: %v", err)
	}
	if err := cl.Get(t.Context(), deletedKey, &corev1.LimitRange{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the object after the failed deletion was not removed (err = %v)", err)
	}
}

// LimitRange cleanup is opportunistic and must not stop namespace creation when its
// cluster-wide List is temporarily unavailable.
func TestReconcileNamespacesSurvivesDisabledLimitRangeSweepFailure(t *testing.T) {
	scheme := limitRangeScheme(t)
	if err := cozyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cozyv1alpha1 to scheme: %v", err)
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*corev1.LimitRangeList); ok {
					return apierrors.NewServiceUnavailable("etcd leader changed")
				}
				return c.List(ctx, list, opts...)
			},
		}).Build()

	r := &PackageReconciler{Client: cl, APIReader: cl, Scheme: scheme}
	pkg := &cozyv1alpha1.Package{ObjectMeta: metav1.ObjectMeta{Name: "platform"}}
	variant := &cozyv1alpha1.Variant{
		Name: "default",
		Components: []cozyv1alpha1.Component{{
			Name:    "active",
			Install: &cozyv1alpha1.ComponentInstall{Namespace: "cozy-active"},
		}},
	}

	if err := r.reconcileNamespaces(t.Context(), pkg, variant); err != nil {
		t.Fatalf("a failed LimitRange sweep must not fail namespace reconciliation: %v", err)
	}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: "cozy-active"}, &corev1.Namespace{}); err != nil {
		t.Fatalf("namespace was not created after the LimitRange sweep failed: %v", err)
	}
}

// The load-bearing containment property: this feature is opportunistic hardening, so no
// failure inside it may stop a system namespace being reconciled. A namespace that never
// appears leaves its components uninstalled, which is worse than the unbounded memory the
// LimitRange is there to bound.
func TestReconcileNamespacesSurvivesLimitRangeFailure(t *testing.T) {
	scheme := limitRangeScheme(t)
	if err := cozyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cozyv1alpha1 to scheme: %v", err)
	}

	var rejected int
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
				if _, ok := obj.(*corev1ac.LimitRangeApplyConfiguration); ok {
					rejected++
					return apierrors.NewForbidden(
						corev1.Resource("limitranges"), SystemDefaultsLimitRangeName,
						fmt.Errorf("denied by policy"))
				}
				return c.Apply(ctx, obj, opts...)
			},
		}).Build()

	r := &PackageReconciler{
		Client:                       cl,
		APIReader:                    cl,
		Scheme:                       scheme,
		SystemNamespaceMemoryLimit:   resource.MustParse("4Gi"),
		SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
	}

	pkg := &cozyv1alpha1.Package{ObjectMeta: metav1.ObjectMeta{Name: "platform"}}
	variant := &cozyv1alpha1.Variant{
		Name: "default",
		Components: []cozyv1alpha1.Component{{
			Name:    "metallb",
			Install: &cozyv1alpha1.ComponentInstall{Namespace: "cozy-metallb"},
		}},
	}

	if err := r.reconcileNamespaces(t.Context(), pkg, variant); err != nil {
		t.Fatalf("a rejected LimitRange must not fail namespace reconciliation: %v", err)
	}
	if rejected != 1 {
		t.Fatalf("injected LimitRange Apply failures = %d, want 1", rejected)
	}

	ns := &corev1.Namespace{}
	if err := cl.Get(t.Context(), types.NamespacedName{Name: "cozy-metallb"}, ns); err != nil {
		t.Fatalf("namespace was not created: %v", err)
	}
	if ns.Labels["cozystack.io/system"] != "true" {
		t.Errorf("system label = %q, want true", ns.Labels["cozystack.io/system"])
	}
}

func TestFindRequestAboveDefaultLimit(t *testing.T) {
	scheme := limitRangeScheme(t)
	controller := true

	terminated := systemPod("migrate-once", "8Gi", "")
	terminated.Status.Phase = corev1.PodSucceeded

	// Failed is NOT skipped, and needs its own fixture to pin that. An eviction or a
	// node crash leaves one of these holding the oversized request, its controller
	// recreates it, and dropping the ceiling meanwhile rejects the replacement for good.
	crashed := systemPod("crashed-once", "9Gi", "")
	crashed.Status.Phase = corev1.PodFailed

	initHeavy := systemPod("with-init", "16Mi", "")
	initHeavy.Spec.InitContainers = []corev1.Container{{
		Name: "prepare",
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("6Gi")},
		},
	}}

	finishedJob := systemJob("migrated-once", "8Gi", "")
	finishedJob.UID = types.UID("migrated-once-uid")
	finishedJob.Status.Conditions = []batchv1.JobCondition{{
		Type:   batchv1.JobComplete,
		Status: corev1.ConditionTrue,
	}}
	failedFinishedJobPod := systemPod("migrated-once-failed", "9Gi", "")
	failedFinishedJobPod.Status.Phase = corev1.PodFailed
	failedFinishedJobPod.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "batch/v1",
		Kind:       "Job",
		Name:       finishedJob.Name,
		UID:        finishedJob.UID,
		Controller: &controller,
	}}
	nonControllingFinishedJobPod := systemPod("mentions-migrated-once", "10Gi", "")
	nonControllingFinishedJobPod.Status.Phase = corev1.PodFailed
	nonControllingFinishedJobPod.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "batch/v1",
		Kind:       "Job",
		Name:       finishedJob.Name,
		UID:        finishedJob.UID,
	}}

	failedJob := systemJob("gave-up", "8Gi", "")
	failedJob.UID = types.UID("gave-up-uid")
	failedJob.Status.Conditions = []batchv1.JobCondition{{
		Type:   batchv1.JobFailed,
		Status: corev1.ConditionTrue,
	}}
	failedFailedJobPod := systemPod("gave-up-failed", "9Gi", "")
	failedFailedJobPod.Status.Phase = corev1.PodFailed
	failedFailedJobPod.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "batch/v1",
		Kind:       "Job",
		Name:       failedJob.Name,
		UID:        failedJob.UID,
		Controller: &controller,
	}}

	activeJob := systemJob("retrying-migration", "1Gi", "")
	activeJob.UID = types.UID("retrying-migration-uid")
	failedActiveJobPod := systemPod("retrying-migration-failed", "9Gi", "")
	failedActiveJobPod.Status.Phase = corev1.PodFailed
	failedActiveJobPod.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "batch/v1",
		Kind:       "Job",
		Name:       activeJob.Name,
		UID:        activeJob.UID,
		Controller: &controller,
	}}

	suspended := true
	suspendedCronJob := systemCronJob("suspended-backup", "7Gi", "")
	suspendedCronJob.Spec.Suspend = &suspended

	tests := []struct {
		name    string
		objects []client.Object
		want    *memoryRequestBlocker
	}{
		{
			name:    "no request above the limit",
			objects: []client.Object{systemPod("metallb-speaker", "512Mi", "")},
		},
		{
			name:    "a request equal to the limit is admissible",
			objects: []client.Object{systemPod("exactly-at", "4Gi", "")},
		},
		{
			// A pod that ran to completion is never recreated from this spec,
			// so it cannot block admission of anything.
			name:    "a succeeded pod does not count",
			objects: []client.Object{terminated},
		},
		{
			// A Failed pod does count. It is frequently the only surviving
			// evidence that the namespace must keep its default withheld, because
			// the shape this scan looks for cannot be admitted once the namespace
			// has settled, so an evicted carcass is all that is left to find.
			name:    "a failed pod still counts",
			objects: []client.Object{crashed},
			want: &memoryRequestBlocker{
				workload:  "Pod/crashed-once",
				container: "crashed-once-app",
				request:   resource.MustParse("9Gi"),
			},
		},
		{
			// LimitRanger defaults init containers on the same terms as
			// regular ones, so the scan has to look at both.
			name:    "an init container counts",
			objects: []client.Object{initHeavy},
			want: &memoryRequestBlocker{
				workload:  "Pod/with-init",
				container: "prepare",
				request:   resource.MustParse("6Gi"),
			},
		},
		{
			// Three offenders, and the largest is deliberately neither the first
			// nor the last one the scan sees: pods are listed in name order, so
			// vmselect-0 sits in the middle. Together with asserting all three
			// reported fields, that is what pins the comparison — keeping the
			// first, the last or the smallest offender instead of the largest
			// each names a different pod and quantity here.
			name: "the largest offender is reported",
			objects: []client.Object{
				systemPod("vmagent-0", "5Gi", ""),
				systemPod("vmselect-0", "9Gi", ""),
				systemPod("vmstorage-0", "6Gi", ""),
			},
			want: &memoryRequestBlocker{
				workload:  "Pod/vmselect-0",
				container: "vmselect-0-app",
				request:   resource.MustParse("9Gi"),
			},
		},
		{
			// The gap a pod-only scan leaves: at replicas: 0 there is no pod to
			// find, so the namespace looks safe, and the request reappears at the
			// moment somebody scales the workload up and its pod is rejected.
			name:    "a scaled-to-zero deployment counts",
			objects: []client.Object{systemDeployment("vmalert", 0, "8Gi", "")},
			want: &memoryRequestBlocker{
				workload:  "Deployment/vmalert",
				container: "vmalert-app",
				request:   resource.MustParse("8Gi"),
			},
		},
		{
			name:    "a scaled-to-zero statefulset counts",
			objects: []client.Object{systemStatefulSet("vmstorage", 0, "8Gi", "")},
			want: &memoryRequestBlocker{
				workload:  "StatefulSet/vmstorage",
				container: "vmstorage-app",
				request:   resource.MustParse("8Gi"),
			},
		},
		{
			// A DaemonSet has no replica count to zero out, but it has no pods
			// either while no node matches it, and its template is still what the
			// next matching node instantiates.
			name:    "a daemonset template counts",
			objects: []client.Object{systemDaemonSet("node-exporter", "6Gi", "")},
			want: &memoryRequestBlocker{
				workload:  "DaemonSet/node-exporter",
				container: "node-exporter-app",
				request:   resource.MustParse("6Gi"),
			},
		},
		{
			// A CronJob between runs has no pods at all, and the next schedule
			// creates one from this template.
			name:    "a cronjob between runs counts",
			objects: []client.Object{systemCronJob("backup", "7Gi", "")},
			want: &memoryRequestBlocker{
				workload:  "CronJob/backup",
				container: "backup-app",
				request:   resource.MustParse("7Gi"),
			},
		},
		{
			// suspend is the same dormancy as replicas: 0 and is undone the same
			// way, so it does not earn an exemption the Deployment case does not get.
			name:    "a suspended cronjob counts",
			objects: []client.Object{suspendedCronJob},
			want: &memoryRequestBlocker{
				workload:  "CronJob/suspended-backup",
				container: "suspended-backup-app",
				request:   resource.MustParse("7Gi"),
			},
		},
		{
			// A Job created standalone — a Helm hook, a migration — has no owner
			// whose template would cover it, and one with spec.suspend has no pods
			// yet either.
			name:    "a job that has not run counts",
			objects: []client.Object{systemJob("migrate", "5Gi", "")},
			want: &memoryRequestBlocker{
				workload:  "Job/migrate",
				container: "migrate-app",
				request:   resource.MustParse("5Gi"),
			},
		},
		{
			// A completed Job never creates another pod, for the same reason a
			// terminated pod is skipped.
			name:    "a finished job does not count",
			objects: []client.Object{finishedJob},
		},
		{
			// The failed child cannot outlive the finished Job as evidence: the
			// controller has definitively stopped and will not replace it.
			name:    "a failed pod owned by a finished job does not count",
			objects: []client.Object{finishedJob, failedFinishedJobPod},
		},
		{
			// Exhausting the Job's retries is just as terminal as completing it:
			// neither its template nor its last failed child can create another pod.
			name:    "a failed pod owned by a job that gave up does not count",
			objects: []client.Object{failedJob, failedFailedJobPod},
		},
		{
			// An owner reference that does not mark the Job as this pod's controller
			// says nothing about whether another controller will replace the pod.
			name:    "a non-controlling finished job reference does not hide a failed pod",
			objects: []client.Object{finishedJob, nonControllingFinishedJobPod},
			want: &memoryRequestBlocker{
				workload:  "Pod/mentions-migrated-once",
				container: "mentions-migrated-once-app",
				request:   resource.MustParse("10Gi"),
			},
		},
		{
			// A failed child of an unfinished Job is still the only evidence of
			// admission-time mutation that may recur on the next retry.
			name:    "a failed pod owned by an unfinished job still counts",
			objects: []client.Object{activeJob, failedActiveJobPod},
			want: &memoryRequestBlocker{
				workload:  "Pod/retrying-migration-failed",
				container: "retrying-migration-failed-app",
				request:   resource.MustParse("9Gi"),
			},
		},
		{
			// A running workload appears twice, as a template and as pods. One
			// blocker comes back, and it names the workload: that is the object an
			// operator has to edit, and it outlives the pod names.
			name: "a workload and its own pods are reported once, as the workload",
			objects: []client.Object{
				systemDeployment("vmselect", 2, "8Gi", ""),
				systemPod("vmselect-6d4b", "8Gi", ""),
			},
			want: &memoryRequestBlocker{
				workload:  "Deployment/vmselect",
				container: "vmselect-app",
				request:   resource.MustParse("8Gi"),
			},
		},
		{
			// The mirror case, and why live pods are still scanned. Not because
			// a webhook raised the request after the template was rendered:
			// LimitRanger defaults the limit before any webhook or policy plugin
			// runs, so a pod one of them touched arrives here already carrying a
			// limit and is skipped. What is left is a pod admitted before the
			// LimitRange existed, or one its controller built from a custom
			// resource no template in this scan can show — either can ask for
			// more than any template in the namespace admits to.
			name: "a request raised on the live pod outranks its template",
			objects: []client.Object{
				systemDeployment("vmselect", 2, "5Gi", ""),
				systemPod("vmselect-6d4b", "9Gi", ""),
			},
			want: &memoryRequestBlocker{
				workload:  "Pod/vmselect-6d4b",
				container: "vmselect-6d4b-app",
				request:   resource.MustParse("9Gi"),
			},
		},
		{
			// Every kind the scan reads, all of them under the limit: the
			// LimitRange has to stay applicable in the ordinary case.
			name: "workloads below the limit do not block",
			objects: []client.Object{
				systemDeployment("grafana", 1, "512Mi", ""),
				systemStatefulSet("vlogs", 1, "1Gi", ""),
				systemDaemonSet("node-exporter", "64Mi", ""),
				systemCronJob("backup", "256Mi", ""),
				systemJob("migrate", "128Mi", ""),
				systemPod("grafana-6d4b", "512Mi", ""),
			},
		},
		{
			// LimitRanger never defaults a container that declares its own memory
			// limit, in a template exactly as in a pod.
			name:    "a template request that carries its own limit does not count",
			objects: []client.Object{systemDeployment("vmstorage", 1, "8Gi", "8Gi")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.objects...).Build()
			r := &PackageReconciler{
				Client:                     cl,
				APIReader:                  cl,
				Scheme:                     scheme,
				SystemNamespaceMemoryLimit: resource.MustParse("4Gi"),
			}

			got, err := r.findRequestAboveDefaultLimit(t.Context(), "cozy-monitoring")
			if err != nil {
				t.Fatalf("findRequestAboveDefaultLimit: %v", err)
			}

			if tt.want == nil {
				if got != nil {
					t.Fatalf("reported %s container %s (%s) as blocking; nothing should block here",
						got.workload, got.container, got.request.String())
				}
				return
			}
			if got == nil {
				t.Fatal("no blocker reported; a container that cannot be admitted under the default was missed")
			}
			if got.workload != tt.want.workload || got.container != tt.want.container ||
				got.request.Cmp(tt.want.request) != 0 {
				t.Errorf("blocker = %s container %s (%s), want %s container %s (%s)",
					got.workload, got.container, got.request.String(),
					tt.want.workload, tt.want.container, tt.want.request.String())
			}
		})
	}
}

// foreignLimitRange builds an administrator's own LimitRange, constraining whichever
// resource and limit kind the case is about.
func foreignLimitRange(name string, limitType corev1.LimitType, field, resourceName, value string) *corev1.LimitRange {
	item := corev1.LimitRangeItem{Type: limitType}
	quantities := corev1.ResourceList{corev1.ResourceName(resourceName): resource.MustParse(value)}
	switch field {
	case "default":
		item.Default = quantities
	case "defaultRequest":
		item.DefaultRequest = quantities
	case "max":
		item.Max = quantities
	case "min":
		item.Min = quantities
	case "ratio":
		item.MaxLimitRequestRatio = quantities
	}
	return &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "cozy-monitoring"},
		Spec:       corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{item}},
	}
}

// Two LimitRanges defaulting container memory in one namespace leave the effective ceiling
// to the order LimitRanger iterates them, which is the hazard this feature cites to keep
// itself out of tenant namespaces. It does not stop being one in a system namespace.
func TestReconcileSystemDefaultsLimitRangeStaysOutOfANamespaceAnotherLimitRangeGoverns(t *testing.T) {
	scheme := limitRangeScheme(t)

	admin := foreignLimitRange("platform-defaults", corev1.LimitTypeContainer, "default", "memory", "512Mi")
	var listed []string
	var writes int
	cl := scanRecordingClient(scheme, &listed, &writes, admin)

	r := &PackageReconciler{
		Client:                       cl,
		APIReader:                    cl,
		Scheme:                       scheme,
		SystemNamespaceMemoryLimit:   resource.MustParse("32Gi"),
		SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
	}

	if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-monitoring"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	err := cl.Get(t.Context(), types.NamespacedName{
		Name:      SystemDefaultsLimitRangeName,
		Namespace: "cozy-monitoring",
	}, &corev1.LimitRange{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("wrote a second container memory default beside %q (err = %v)", admin.Name, err)
	}
	if writes != 0 {
		t.Errorf("%d writes into a namespace another LimitRange already governs, want none", writes)
	}
}

// The sharper half of the same guard. This reconciler writes a default and no max, so an
// administrator's max below the configured default would have every container that declares
// no limit of its own rejected: the default is written first, then validated against the max
// it exceeds. That takes out the whole namespace, and it reaches kube-system.
func TestReconcileSystemDefaultsLimitRangeStaysUnderAForeignContainerMemoryMax(t *testing.T) {
	scheme := limitRangeScheme(t)

	admin := foreignLimitRange("tight-ceiling", corev1.LimitTypeContainer, "max", "memory", "2Gi")
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(admin).Build()

	r := &PackageReconciler{
		Client:                       cl,
		APIReader:                    cl,
		Scheme:                       scheme,
		SystemNamespaceMemoryLimit:   resource.MustParse("32Gi"),
		SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
	}

	if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-monitoring"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	err := cl.Get(t.Context(), types.NamespacedName{
		Name:      SystemDefaultsLimitRangeName,
		Namespace: "cozy-monitoring",
	}, &corev1.LimitRange{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("defaulted 32Gi into a namespace capped at 2Gi, which rejects every container "+
			"that declares no limit of its own (err = %v)", err)
	}
}

// A foreign LimitRange appearing after this one was already applied has to be resolved by
// withdrawing, not by sitting beside it: the object this reconciler wrote is half of the
// ambiguity, so leaving it is leaving the hazard.
func TestReconcileSystemDefaultsLimitRangeWithdrawsWhenAForeignPolicyAppears(t *testing.T) {
	scheme := limitRangeScheme(t)

	ours := (&PackageReconciler{
		SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
	}).systemDefaultsLimitRange("cozy-monitoring", resource.MustParse("32Gi"))
	admin := foreignLimitRange("platform-defaults", corev1.LimitTypeContainer, "default", "memory", "512Mi")
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ours, admin).Build()

	r := &PackageReconciler{
		Client:                       cl,
		APIReader:                    cl,
		Scheme:                       scheme,
		SystemNamespaceMemoryLimit:   resource.MustParse("32Gi"),
		SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
	}

	if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-monitoring"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	err := cl.Get(t.Context(), types.NamespacedName{
		Name:      SystemDefaultsLimitRangeName,
		Namespace: "cozy-monitoring",
	}, &corev1.LimitRange{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("kept its own default beside %q (err = %v)", admin.Name, err)
	}

	// The administrator's object is not this reconciler's to touch, in either direction.
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name:      admin.Name,
		Namespace: "cozy-monitoring",
	}, &corev1.LimitRange{}); err != nil {
		t.Errorf("removed the administrator's own LimitRange: %v", err)
	}
}

// The guard is narrow on the resource and broad on everything else, and both halves need
// pinning. A LimitRange bounding cpu is no business of this reconciler and must coexist,
// or the guard becomes a blanket opt-out any unrelated policy can trip.
func TestReconcileSystemDefaultsLimitRangeIgnoresLimitRangesThatDoNotTouchMemory(t *testing.T) {
	scheme := limitRangeScheme(t)
	pvcPolicy := foreignLimitRange("pvc-policy", corev1.LimitTypePersistentVolumeClaim, "max", "memory", "2Gi")
	pvcPolicy.Spec.Limits[0].Max[corev1.ResourceStorage] = resource.MustParse("1Ti")

	for _, tc := range []struct {
		name    string
		foreign *corev1.LimitRange
	}{
		{"container cpu default", foreignLimitRange("cpu-policy", corev1.LimitTypeContainer, "default", "cpu", "500m")},
		{"container cpu max", foreignLimitRange("cpu-ceiling", corev1.LimitTypeContainer, "max", "cpu", "2")},
		{"pod cpu max", foreignLimitRange("pod-cpu", corev1.LimitTypePod, "max", "cpu", "4")},
		{"persistent volume claim memory max", pvcPolicy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc.foreign).Build()

			r := &PackageReconciler{
				Client:                       cl,
				APIReader:                    cl,
				Scheme:                       scheme,
				SystemNamespaceMemoryLimit:   resource.MustParse("32Gi"),
				SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
			}

			if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-monitoring"); err != nil {
				t.Fatalf("reconcile: %v", err)
			}

			if got := limitRangeDefaultMemory(t, cl, "cozy-monitoring"); got.Cmp(resource.MustParse("32Gi")) != 0 {
				t.Errorf("default memory = %s, want the configured 32Gi; %q says nothing about memory "+
					"and must not withhold it", got.String(), tc.foreign.Name)
			}
		})
	}
}

// The other half. Every memory-bearing field withholds, at either scope, because each one
// can reject the pods this reconciler's own default and defaultRequest would produce:
//
//   - a container max below the default, or a min above the defaultRequest, rejects directly;
//   - a maxLimitRequestRatio rejects because 32Gi against 32Mi is a ratio of 1024:1;
//   - a Pod-scoped bound cannot even be evaluated here, since whether a per-container default
//     breaches it depends on how many containers a pod that does not exist yet will have.
//
// The last one is why this is an ownership rule and not arithmetic. Checking only the fields
// whose collision is computable leaves the namespace-wide rejection reachable through the
// fields whose collision is not.
func TestReconcileSystemDefaultsLimitRangeWithholdsForAnyForeignMemoryPolicy(t *testing.T) {
	scheme := limitRangeScheme(t)

	for _, tc := range []struct {
		name    string
		foreign *corev1.LimitRange
	}{
		{"container memory default", foreignLimitRange("mem-default", corev1.LimitTypeContainer, "default", "memory", "512Mi")},
		{"container memory defaultRequest", foreignLimitRange("mem-defreq", corev1.LimitTypeContainer, "defaultRequest", "memory", "64Mi")},
		{"container memory max", foreignLimitRange("mem-max", corev1.LimitTypeContainer, "max", "memory", "2Gi")},
		{"container memory min", foreignLimitRange("mem-min", corev1.LimitTypeContainer, "min", "memory", "128Mi")},
		{"container memory ratio", foreignLimitRange("mem-ratio", corev1.LimitTypeContainer, "ratio", "memory", "4")},
		{"pod memory max", foreignLimitRange("pod-mem-max", corev1.LimitTypePod, "max", "memory", "2Gi")},
		{"pod memory min", foreignLimitRange("pod-mem-min", corev1.LimitTypePod, "min", "memory", "1Gi")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc.foreign).Build()

			r := &PackageReconciler{
				Client:                       cl,
				APIReader:                    cl,
				Scheme:                       scheme,
				SystemNamespaceMemoryLimit:   resource.MustParse("32Gi"),
				SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
			}

			if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-monitoring"); err != nil {
				t.Fatalf("reconcile: %v", err)
			}

			err := cl.Get(t.Context(), types.NamespacedName{
				Name:      SystemDefaultsLimitRangeName,
				Namespace: "cozy-monitoring",
			}, &corev1.LimitRange{})
			if !apierrors.IsNotFound(err) {
				t.Errorf("wrote a container memory default beside %q, which already constrains memory (err = %v)",
					tc.foreign.Name, err)
			}
		})
	}
}

// Identity is the managed-by label, not the name. The name is not reserved, so an
// administrator's LimitRange can legitimately be called cozystack-system-defaults. This
// fixture deliberately says nothing about memory: a differently named cpu policy coexists,
// but an object holding this exact name cannot coexist, and the forced apply must not replace
// its atomic Limits list merely because the memory-policy guard has no reason to notice it.
func TestReconcileSystemDefaultsLimitRangeDoesNotOverwriteAForeignObjectSharingItsName(t *testing.T) {
	scheme := limitRangeScheme(t)

	foreign := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SystemDefaultsLimitRangeName,
			Namespace: "kube-system",
			// No managed-by label: this is not ours.
		},
		Spec: corev1.LimitRangeSpec{
			Limits: []corev1.LimitRangeItem{{
				Type:    corev1.LimitTypeContainer,
				Default: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")},
			}},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(foreign).Build()

	r := &PackageReconciler{
		Client:                       cl,
		APIReader:                    cl,
		Scheme:                       scheme,
		SystemNamespaceMemoryLimit:   resource.MustParse("32Gi"),
		SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
	}

	if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "kube-system"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := &corev1.LimitRange{}
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name:      SystemDefaultsLimitRangeName,
		Namespace: "kube-system",
	}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Spec.Limits) != 1 {
		t.Fatalf("len(limits) = %d, want the administrator's single item untouched", len(got.Spec.Limits))
	}
	if _, ok := got.Spec.Limits[0].Default[corev1.ResourceCPU]; !ok {
		t.Errorf("the administrator's cpu default was replaced; spec = %+v", got.Spec.Limits[0])
	}
	if _, ok := got.Spec.Limits[0].Default[corev1.ResourceMemory]; ok {
		t.Errorf("the operator's memory default was merged into the administrator's object; spec = %+v", got.Spec.Limits[0])
	}
	if got.Labels[managedByLabel] == packageControllerFieldOwner {
		t.Errorf("stamped this reconciler's managed-by label onto an object it did not create, "+
			"which would then let the disable path delete it; labels = %v", got.Labels)
	}
}

// A same-named LimitRange without this reconciler's label is ambiguous by construction: it is
// either an administrator's object, or one of ours whose label was removed, and nothing here
// can tell those apart. The two mistakes are not symmetric. Adopting it means the disable path
// later deletes an administrator's policy, silently. Leaving it means a namespace keeps a
// default this reconciler no longer manages, which still carries the memory.max the feature
// exists to provide and is now said out loud in the log.
//
// So the ambiguous object is treated as foreign: not overwritten, not adopted, not deleted.
// This replaces an earlier case that asserted the label was re-stamped, which was the adopting
// behaviour, before it was clear that the same code path reaches an administrator's object.
func TestReconcileSystemDefaultsLimitRangeLeavesAnUnlabelledSameNamedObjectAlone(t *testing.T) {
	scheme := limitRangeScheme(t)

	stripped := (&PackageReconciler{
		SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
	}).systemDefaultsLimitRange("cozy-monitoring", resource.MustParse("4Gi"))
	stripped.Labels = nil

	var listed []string
	var writes int
	cl := scanRecordingClient(scheme, &listed, &writes, stripped)

	r := &PackageReconciler{
		Client:                       cl,
		APIReader:                    cl,
		Scheme:                       scheme,
		SystemNamespaceMemoryLimit:   resource.MustParse("32Gi"),
		SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
	}

	if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-monitoring"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := &corev1.LimitRange{}
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name:      SystemDefaultsLimitRangeName,
		Namespace: "cozy-monitoring",
	}, got); err != nil {
		t.Fatalf("the ambiguous object was deleted: %v", err)
	}
	if d := got.Spec.Limits[0].Default[corev1.ResourceMemory]; d.Cmp(resource.MustParse("4Gi")) != 0 {
		t.Errorf("default memory = %s, want the object left at its own 4Gi rather than rewritten to 32Gi", d.String())
	}
	if got.Labels[managedByLabel] == packageControllerFieldOwner {
		t.Error("adopted the object by stamping the managed-by label; the disable path would then delete it")
	}
	if writes != 0 {
		t.Errorf("%d writes against an object of ambiguous ownership, want none", writes)
	}
}

// The shape this default cannot be applied over, and what happens instead.
//
// A container requesting more than the configured limit with no limit of its own would be
// defaulted that limit and then rejected for requesting more than it, so the namespace is
// left without a default rather than given one that stops its pods. Withholding fails in the
// safe direction: no memory.max is how every release before this behaved, while a default
// below a request is a namespace that cannot start the workload at all.
func TestReconcileSystemDefaultsLimitRangeWithholdsFromAnOversizedRequest(t *testing.T) {
	scheme := limitRangeScheme(t)

	for _, tc := range []struct {
		name    string
		seeded  bool
		blocker client.Object
	}{
		{"nothing written yet", false, systemPod("keycloak-db-1", "8Gi", "")},
		{"a default already written, which has to be withdrawn", true, systemPod("keycloak-db-1", "8Gi", "")},
		{"the blocker is a workload template with no pods", false, systemDeployment("vmstorage", 0, "9Gi", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := []client.Object{tc.blocker}
			if tc.seeded {
				objects = append(objects, (&PackageReconciler{
					SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
				}).systemDefaultsLimitRange("cozy-monitoring", resource.MustParse("4Gi")))
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()

			r := &PackageReconciler{
				Client:                       cl,
				APIReader:                    cl,
				Scheme:                       scheme,
				SystemNamespaceMemoryLimit:   resource.MustParse("4Gi"),
				SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
			}

			if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-monitoring"); err != nil {
				t.Fatalf("reconcile: %v", err)
			}

			err := cl.Get(t.Context(), types.NamespacedName{
				Name:      SystemDefaultsLimitRangeName,
				Namespace: "cozy-monitoring",
			}, &corev1.LimitRange{})
			if !apierrors.IsNotFound(err) {
				t.Errorf("namespace holds a default beside a request above it, which rejects that "+
					"workload at admission (err = %v)", err)
			}
		})
	}
}

// And it self-heals with no state and no timer, which is the whole reason withholding replaced
// the raise: once the oversized request is gone the scan finds nothing and the default is
// applied on the next reconcile. Nothing has to be remembered between the two.
func TestReconcileSystemDefaultsLimitRangeAppliesOnceTheOversizedRequestIsGone(t *testing.T) {
	scheme := limitRangeScheme(t)

	blocker := systemPod("keycloak-db-1", "8Gi", "")
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(blocker).Build()

	r := &PackageReconciler{
		Client:                       cl,
		APIReader:                    cl,
		Scheme:                       scheme,
		SystemNamespaceMemoryLimit:   resource.MustParse("4Gi"),
		SystemNamespaceMemoryRequest: resource.MustParse("32Mi"),
	}

	if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-monitoring"); err != nil {
		t.Fatalf("reconcile while withheld: %v", err)
	}
	if err := cl.Get(t.Context(), types.NamespacedName{
		Name:      SystemDefaultsLimitRangeName,
		Namespace: "cozy-monitoring",
	}, &corev1.LimitRange{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected the namespace withheld, got err = %v", err)
	}

	if err := cl.Delete(t.Context(), blocker); err != nil {
		t.Fatalf("delete the blocker: %v", err)
	}
	if err := r.reconcileSystemDefaultsLimitRange(t.Context(), "cozy-monitoring"); err != nil {
		t.Fatalf("reconcile after the blocker went: %v", err)
	}
	if got := limitRangeDefaultMemory(t, cl, "cozy-monitoring"); got.Cmp(resource.MustParse("4Gi")) != 0 {
		t.Errorf("default memory = %s, want the configured 4Gi applied once nothing blocks it", got.String())
	}
}
