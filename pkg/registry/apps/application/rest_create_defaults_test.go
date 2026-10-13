/*
Copyright 2024 The Cozystack Authors.

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

package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/warning"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

const (
	workerDefaultsAll = `
_cluster:
  cluster-domain: cozy.local
  worker-image:
    catalog: "true"
    factory-url: "https://factory.example.org"
    installer-repository: "factory.example.org/installer"
`
	workerDefaultsNoCatalog = `
_cluster:
  worker-image:
    factory-url: "https://factory.example.org"
    installer-repository: "factory.example.org/installer"
`
	workerDefaultsFactoryOnly = `
_cluster:
  worker-image:
    factory-url: "https://factory.example.org"
`
	workerDefaultsCatalogOnly = `
_cluster:
  worker-image:
    catalog: "true"
`
	workerDefaultsCatalogOff = `
_cluster:
  worker-image:
    catalog: "false"
    factory-url: "https://factory.example.org"
`
	workerDefaultsInstallerOnly = `
_cluster:
  worker-image:
    installer-repository: "factory.example.org/installer"
`
	workerDefaultsNone = `
_cluster:
  cluster-domain: cozy.local
  branding:
    brandName: Acme
`
)

func platformValuesFixture(values string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: platformValuesSecret, Namespace: platformValuesNamespace},
		Data:       map[string][]byte{platformValuesKey: []byte(values)},
	}
}

func newCreateDefaultsREST(t *testing.T, kind string, objs ...client.Object) *REST {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("register helmv2 scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("register corev1 scheme: %v", err)
	}
	resourceCfg := &config.ResourceConfig{
		Resources: []config.Resource{{Application: config.ApplicationConfig{Kind: kind}}},
	}
	if err := appsv1alpha1.RegisterDynamicTypes(scheme, resourceCfg); err != nil {
		t.Fatalf("register dynamic types: %v", err)
	}
	return &REST{
		c: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build(),
		gvr: schema.GroupVersionResource{
			Group:    appsv1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: "kubernetesnodes",
		},
		gvk: schema.GroupVersionKind{
			Group:   appsv1alpha1.GroupName,
			Version: "v1alpha1",
			Kind:    kind,
		},
		kindName:      kind,
		releaseConfig: config.ReleaseConfig{Prefix: "kubernetes-nodes-"},
	}
}

// failSecretReads makes every Get of a Secret return err and leaves the rest of the
// REST's client as it was.
func failSecretReads(r *REST, err error) {
	r.c = interceptor.NewClient(r.c.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				return err
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
}

func newPoolApp(spec string) *appsv1alpha1.Application {
	app := &appsv1alpha1.Application{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps.cozystack.io/v1alpha1", Kind: kubernetesNodesKind},
		ObjectMeta: metav1.ObjectMeta{Name: "myk8s-md0", Namespace: "tenant-foo"},
	}
	if spec != "" {
		app.Spec = &apiextv1.JSON{Raw: []byte(spec)}
	}
	return app
}

const (
	factoryURL    = "https://factory.example.org"
	installerRepo = "factory.example.org/installer"
)

func TestApplyCreateDefaults(t *testing.T) {
	tests := []struct {
		name   string
		kind   string
		secret *corev1.Secret
		spec   string
		// getErr, when set, is what reading the platform values Secret returns.
		getErr error
		// want is the spec after the call; "" means the spec must still be nil.
		want string
		// wantErr is a part of the error the call must return; "" means no error.
		wantErr string
	}{
		{
			name:   "fills every default into an empty spec",
			secret: platformValuesFixture(workerDefaultsAll),
			want:   `{"osImage":{"builtin":{}},"talos":{"imageFactoryURL":"` + factoryURL + `","installerRepository":"` + installerRepo + `"}}`,
		},
		{
			name:   "fills around what the spec already carries",
			secret: platformValuesFixture(workerDefaultsAll),
			spec:   `{"diskSize":"30Gi","maxReplicas":3,"minReplicas":0,"talos":{"version":"v1.13.6"}}`,
			want:   `{"diskSize":"30Gi","maxReplicas":3,"minReplicas":0,"osImage":{"builtin":{}},"talos":{"imageFactoryURL":"` + factoryURL + `","installerRepository":"` + installerRepo + `","version":"v1.13.6"}}`,
		},
		{
			name:   "leaves an explicit factory osImage alone",
			secret: platformValuesFixture(workerDefaultsAll),
			spec:   `{"osImage":{"factory":{}}}`,
			want:   `{"osImage":{"factory":{}},"talos":{"imageFactoryURL":"` + factoryURL + `","installerRepository":"` + installerRepo + `"}}`,
		},
		{
			name:   "leaves an empty osImage alone",
			secret: platformValuesFixture(workerDefaultsAll),
			spec:   `{"osImage":{}}`,
			want:   `{"osImage":{},"talos":{"imageFactoryURL":"` + factoryURL + `","installerRepository":"` + installerRepo + `"}}`,
		},
		{
			name:   "leaves the talos fields the spec sets alone, an empty one included",
			secret: platformValuesFixture(workerDefaultsAll),
			spec:   `{"talos":{"imageFactoryURL":"https://mine.example.org","installerRepository":""}}`,
			want:   `{"osImage":{"builtin":{}},"talos":{"imageFactoryURL":"https://mine.example.org","installerRepository":""}}`,
		},
		{
			name:   "treats null as unset",
			secret: platformValuesFixture(workerDefaultsAll),
			spec:   `{"osImage":null,"talos":null}`,
			want:   `{"osImage":{"builtin":{}},"talos":{"imageFactoryURL":"` + factoryURL + `","installerRepository":"` + installerRepo + `"}}`,
		},
		{
			name:   "gives a proxmox pool the installer and nothing about images",
			secret: platformValuesFixture(workerDefaultsAll),
			spec:   `{"substrate":"proxmox"}`,
			want:   `{"substrate":"proxmox","talos":{"installerRepository":"` + installerRepo + `"}}`,
		},
		{
			name:   "does not clone a golden when the catalog is off",
			secret: platformValuesFixture(workerDefaultsNoCatalog),
			want:   `{"talos":{"imageFactoryURL":"` + factoryURL + `","installerRepository":"` + installerRepo + `"}}`,
		},
		{
			name:   "writes only what the platform set",
			secret: platformValuesFixture(workerDefaultsFactoryOnly),
			want:   `{"talos":{"imageFactoryURL":"` + factoryURL + `"}}`,
		},
		{
			name:   "clones a golden and writes nothing else when the catalog is all the platform set",
			secret: platformValuesFixture(workerDefaultsCatalogOnly),
			want:   `{"osImage":{"builtin":{}}}`,
		},
		{
			name:   "leaves a proxmox pool as it is when the catalog is all the platform set",
			secret: platformValuesFixture(workerDefaultsCatalogOnly),
			spec:   `{"substrate":"proxmox"}`,
			want:   `{"substrate":"proxmox"}`,
		},
		{
			name:   "does not clone a golden when the catalog flag is anything but true",
			secret: platformValuesFixture(workerDefaultsCatalogOff),
			want:   `{"talos":{"imageFactoryURL":"` + factoryURL + `"}}`,
		},
		{
			name:   "reads a spec that is JSON null as an empty one",
			secret: platformValuesFixture(workerDefaultsAll),
			spec:   `null`,
			want:   `{"osImage":{"builtin":{}},"talos":{"imageFactoryURL":"` + factoryURL + `","installerRepository":"` + installerRepo + `"}}`,
		},
		{
			name:   "writes the installer alone, with no factory URL",
			secret: platformValuesFixture(workerDefaultsInstallerOnly),
			want:   `{"talos":{"installerRepository":"` + installerRepo + `"}}`,
		},
		{
			name:   "leaves the spec nil when the platform sets nothing",
			secret: platformValuesFixture(workerDefaultsNone),
		},
		{
			name:   "leaves the spec as it came when the platform sets nothing",
			secret: platformValuesFixture(workerDefaultsNone),
			spec:   `{"diskSize":"30Gi"}`,
			want:   `{"diskSize":"30Gi"}`,
		},
		{
			name:   "leaves other kinds alone",
			kind:   "Kubernetes",
			secret: platformValuesFixture(workerDefaultsAll),
		},
		{
			name: "leaves the spec alone when the platform values Secret is missing",
		},
		{
			name:    "fails when the platform values are unreadable",
			secret:  platformValuesFixture("_cluster: [not, a, map"),
			spec:    `{"diskSize":"30Gi"}`,
			want:    `{"diskSize":"30Gi"}`,
			wantErr: "does not hold readable values.yaml",
		},
		{
			name:    "fails when the platform values Secret cannot be read",
			secret:  platformValuesFixture(workerDefaultsAll),
			spec:    `{"diskSize":"30Gi"}`,
			getErr:  apierrors.NewServiceUnavailable("apiserver is restarting"),
			want:    `{"diskSize":"30Gi"}`,
			wantErr: "cannot read the platform worker pool defaults",
		},
		{
			name:    "fails when the platform values Secret is forbidden",
			secret:  platformValuesFixture(workerDefaultsAll),
			getErr:  apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, platformValuesSecret, errors.New("no access")),
			wantErr: "cannot read the platform worker pool defaults",
		},
		{
			name:   "leaves a talos that is not an object to validation",
			secret: platformValuesFixture(workerDefaultsAll),
			spec:   `{"talos":"nope"}`,
			want:   `{"osImage":{"builtin":{}},"talos":"nope"}`,
		},
		{
			name:   "leaves a spec that is not an object alone",
			secret: platformValuesFixture(workerDefaultsAll),
			spec:   `["not","an","object"]`,
			want:   `["not","an","object"]`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kind := tc.kind
			if kind == "" {
				kind = kubernetesNodesKind
			}
			var objs []client.Object
			if tc.secret != nil {
				objs = append(objs, tc.secret)
			}
			r := newCreateDefaultsREST(t, kind, objs...)
			if tc.getErr != nil {
				failSecretReads(r, tc.getErr)
			}
			app := newPoolApp(tc.spec)

			err := r.applyCreateDefaults(context.Background(), app)

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("applyCreateDefaults: %v", err)
			}
			if tc.want == "" {
				if app.Spec != nil {
					t.Fatalf("spec = %s, want it untouched (nil)", app.Spec.Raw)
				}
				return
			}
			if app.Spec == nil {
				t.Fatalf("spec is nil, want %s", tc.want)
			}
			assertJSONEqual(t, tc.want, app.Spec.Raw)
		})
	}
}

// A number the spec carries goes back out as written. assertJSONEqual cannot tell,
// since it reads both sides as float64, so the bytes are looked at.
func TestApplyCreateDefaults_KeepsNumbersAsWritten(t *testing.T) {
	r := newCreateDefaultsREST(t, kubernetesNodesKind, platformValuesFixture(workerDefaultsAll))
	app := newPoolApp(`{"maxReplicas":12345678901234567890,"minReplicas":1e400}`)

	if err := r.applyCreateDefaults(context.Background(), app); err != nil {
		t.Fatalf("applyCreateDefaults: %v", err)
	}

	for _, want := range []string{`"maxReplicas":12345678901234567890`, `"minReplicas":1e400`, `"osImage":{"builtin":{}}`} {
		if !strings.Contains(string(app.Spec.Raw), want) {
			t.Errorf("spec = %s, want it to contain %s", app.Spec.Raw, want)
		}
	}
}

func assertJSONEqual(t *testing.T, want string, got []byte) {
	t.Helper()
	var w, g any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want is not JSON: %v", err)
	}
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got is not JSON: %v (%s)", err, got)
	}
	wb, _ := json.Marshal(w)
	gb, _ := json.Marshal(g)
	if string(wb) != string(gb) {
		t.Fatalf("spec = %s, want %s", gb, wb)
	}
}

func storedValues(t *testing.T, r *REST, name string) string {
	t.Helper()
	hr := &helmv2.HelmRelease{}
	if err := r.c.Get(context.Background(), client.ObjectKey{Namespace: "tenant-foo", Name: r.releaseConfig.Prefix + name}, hr); err != nil {
		t.Fatalf("get stored HelmRelease: %v", err)
	}
	if hr.Spec.Values == nil {
		return ""
	}
	return string(hr.Spec.Values.Raw)
}

// The defaults must be in the stored HelmRelease values, not only in the response:
// a pool renders from those values for the rest of its life.
func TestCreate_StoresPlatformDefaultsInTheHelmRelease(t *testing.T) {
	r := newCreateDefaultsREST(t, kubernetesNodesKind, platformValuesFixture(workerDefaultsAll))
	ctx := request.WithNamespace(context.Background(), "tenant-foo")

	if _, err := r.Create(ctx, newPoolApp(`{"cluster":"myk8s"}`), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	assertJSONEqual(t,
		`{"cluster":"myk8s","osImage":{"builtin":{}},"talos":{"imageFactoryURL":"`+factoryURL+`","installerRepository":"`+installerRepo+`"}}`,
		[]byte(storedValues(t, r, "myk8s-md0")))
}

// A null the platform has a default for is filled before the null normalization sees
// it, so the client is not told that the chart default applies to a field that got the
// platform's. A null the platform says nothing about is still normalized and warned.
func TestCreate_FillsANullBeforeItIsNormalized(t *testing.T) {
	r := newCreateDefaultsREST(t, kubernetesNodesKind, platformValuesFixture(workerDefaultsAll))
	r.specSchema = chartSpecSchema(t, "kubernetes-nodes")
	rec := &fakeWarningRecorder{}
	ctx := warning.WithWarningRecorder(request.WithNamespace(context.Background(), "tenant-foo"), rec)

	spec := `{"cluster":"myk8s","talos":{"installerRepository":null,"registryMirrors":null}}`
	if _, err := r.Create(ctx, newPoolApp(spec), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	assertJSONEqual(t,
		`{"cluster":"myk8s","osImage":{"builtin":{}},"talos":{"imageFactoryURL":"`+factoryURL+`","installerRepository":"`+installerRepo+`"}}`,
		[]byte(storedValues(t, r, "myk8s-md0")))
	if len(rec.warnings) != 1 || !strings.Contains(rec.warnings[0], "talos.registryMirrors") || strings.Contains(rec.warnings[0], "installerRepository") {
		t.Fatalf("warnings = %q, want one that names talos.registryMirrors and not talos.installerRepository", rec.warnings)
	}
}

// kubectl apply of an object that does not exist yet reaches Create through Update.
func TestUpdate_OfAMissingObjectCreatesWithDefaults(t *testing.T) {
	r := newCreateDefaultsREST(t, kubernetesNodesKind, platformValuesFixture(workerDefaultsAll))
	ctx := request.WithNamespace(context.Background(), "tenant-foo")

	_, created, err := r.Update(ctx, "myk8s-md0", newDefaultUpdatedObjectInfo(newPoolApp(`{"cluster":"myk8s"}`)), nil, nil, true, &metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !created {
		t.Fatal("Update reported the object as updated, want created")
	}

	assertJSONEqual(t,
		`{"cluster":"myk8s","osImage":{"builtin":{}},"talos":{"imageFactoryURL":"`+factoryURL+`","installerRepository":"`+installerRepo+`"}}`,
		[]byte(storedValues(t, r, "myk8s-md0")))
}

// An existing pool must never pick the defaults up: they are hashed into names the
// MachineDeployment points at, and a pool that gained one would roll.
func TestUpdate_OfAnExistingObjectDoesNotApplyDefaults(t *testing.T) {
	existing := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubernetes-nodes-myk8s-md0",
			Namespace: "tenant-foo",
			Labels: map[string]string{
				ApplicationKindLabel:  kubernetesNodesKind,
				ApplicationGroupLabel: appsv1alpha1.GroupName,
				ApplicationNameLabel:  "myk8s-md0",
			},
		},
		Spec: helmv2.HelmReleaseSpec{
			Values: &apiextv1.JSON{Raw: []byte(`{"cluster":"myk8s"}`)},
		},
	}
	r := newCreateDefaultsREST(t, kubernetesNodesKind, platformValuesFixture(workerDefaultsAll), existing)
	ctx := request.WithNamespace(context.Background(), "tenant-foo")

	_, created, err := r.Update(ctx, "myk8s-md0", newDefaultUpdatedObjectInfo(newPoolApp(`{"cluster":"myk8s","maxReplicas":5}`)), nil, nil, false, &metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if created {
		t.Fatal("Update reported the object as created, want updated")
	}

	assertJSONEqual(t, `{"cluster":"myk8s","maxReplicas":5}`, []byte(storedValues(t, r, "myk8s-md0")))
}

// A platform with no values Secret has set nothing, and the create goes on.
func TestCreate_SucceedsWithoutPlatformValues(t *testing.T) {
	r := newCreateDefaultsREST(t, kubernetesNodesKind)
	ctx := request.WithNamespace(context.Background(), "tenant-foo")

	if _, err := r.Create(ctx, newPoolApp(`{"cluster":"myk8s"}`), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	assertJSONEqual(t, `{"cluster":"myk8s"}`, []byte(storedValues(t, r, "myk8s-md0")))
}

// A Secret that cannot be read is not the same as a platform that set nothing: storing
// the pool without the defaults would pick its image source for good, so the create is
// refused with a 503, which a caller can repeat once the apiserver answers, and
// nothing is stored.
func TestCreate_FailsWhenThePlatformValuesCannotBeRead(t *testing.T) {
	r := newCreateDefaultsREST(t, kubernetesNodesKind, platformValuesFixture(workerDefaultsAll))
	failSecretReads(r, errors.New("etcdserver: request timed out"))
	ctx := request.WithNamespace(context.Background(), "tenant-foo")

	_, err := r.Create(ctx, newPoolApp(`{"cluster":"myk8s"}`), nil, &metav1.CreateOptions{})
	if !apierrors.IsServiceUnavailable(err) {
		t.Fatalf("Create error = %v, want a ServiceUnavailable", err)
	}

	hr := &helmv2.HelmRelease{}
	getErr := r.c.Get(context.Background(), client.ObjectKey{Namespace: "tenant-foo", Name: r.releaseConfig.Prefix + "myk8s-md0"}, hr)
	if !apierrors.IsNotFound(getErr) {
		t.Fatalf("HelmRelease lookup = %v, want NotFound: the refused create must store nothing", getErr)
	}
}
