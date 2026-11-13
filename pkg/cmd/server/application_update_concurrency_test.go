package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"testing"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	fluxshard "github.com/cozystack/cozystack/internal/fluxshardoperator"
	"github.com/cozystack/cozystack/pkg/config"
)

func TestApplicationApplyCreate(t *testing.T) {
	for _, uid := range []string{"", "nonexistent"} {
		for _, dryRun := range []bool{false, true} {
			for _, force := range []bool{false, true} {
				t.Run(fmt.Sprintf("uid=%s/dryRun=%v/force=%v", uid, dryRun, force), func(t *testing.T) {
					ts, backing := startAppsServer(t, &config.ResourceConfig{Resources: []config.Resource{redisConfig}})
					payload := fmt.Sprintf(`{"apiVersion":"apps.cozystack.io/v1alpha1","kind":"Redis","metadata":{"name":"new-app","namespace":"tenant-a","uid":%q},"spec":{"replicas":1}}`, uid)
					url := ts.URL + fmt.Sprintf("/apis/apps.cozystack.io/v1alpha1/namespaces/tenant-a/redises/new-app?fieldManager=create-test&force=%v", force)
					if dryRun {
						url += "&dryRun=All"
					}
					req, err := http.NewRequest(http.MethodPatch, url, bytes.NewBufferString(payload))
					if err != nil {
						t.Fatal(err)
					}
					req.Header.Set("Content-Type", "application/apply-patch+yaml")
					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					response, err := io.ReadAll(resp.Body)
					if closeErr := resp.Body.Close(); closeErr != nil {
						t.Fatal(closeErr)
					}
					if err != nil {
						t.Fatal(err)
					}
					want := http.StatusCreated
					if uid != "" {
						want = http.StatusConflict
					}
					if resp.StatusCode != want {
						t.Fatalf("HTTP %d, want %d: %s", resp.StatusCode, want, response)
					}
					live := &helmv2.HelmRelease{}
					err = backing.Get(context.Background(), client.ObjectKey{Namespace: "tenant-a", Name: "redis-new-app"}, live)
					if dryRun || uid != "" {
						if !apierrors.IsNotFound(err) {
							t.Fatalf("unexpected persisted object: err=%v live=%#v", err, live)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Contains(live.Spec.Values.Raw, []byte(`"replicas":1`)) {
						t.Fatalf("lost apply spec: %s", live.Spec.Values.Raw)
					}
				})
			}
		}
	}
}

type updateAdmissionProbe struct {
	*admission.Handler
	mu          sync.Mutex
	mutated     []string
	validated   []string
	rejectFresh bool
	dryRun      bool
}

func (p *updateAdmissionProbe) Admit(_ context.Context, attrs admission.Attributes, _ admission.ObjectInterfaces) error {
	if attrs.IsDryRun() != p.dryRun {
		return fmt.Errorf("mutating admission lost dry-run option")
	}
	old := attrs.GetOldObject().(*appsv1alpha1.Application)
	p.mu.Lock()
	p.mutated = append(p.mutated, old.ResourceVersion)
	p.mu.Unlock()
	app := attrs.GetObject().(*appsv1alpha1.Application)
	if app.Annotations == nil {
		app.Annotations = map[string]string{}
	}
	app.Annotations["admitted-version"] = old.ResourceVersion
	return nil
}

func (p *updateAdmissionProbe) Validate(_ context.Context, attrs admission.Attributes, _ admission.ObjectInterfaces) error {
	if attrs.IsDryRun() != p.dryRun {
		return fmt.Errorf("validating admission lost dry-run option")
	}
	old := attrs.GetOldObject().(*appsv1alpha1.Application)
	p.mu.Lock()
	p.validated = append(p.validated, old.ResourceVersion)
	reject := p.rejectFresh && len(p.mutated) > 0 && old.ResourceVersion != p.mutated[0]
	p.mu.Unlock()
	if reject {
		return apierrors.NewForbidden(attrs.GetResource().GroupResource(), attrs.GetName(), fmt.Errorf("fresh admission snapshot rejected"))
	}
	return nil
}

func (p *updateAdmissionProbe) versions() ([]string, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.mutated...), append([]string(nil), p.validated...)
}

func TestApplicationUpdateHTTPConcurrency(t *testing.T) {
	for _, method := range []string{"put", "json", "merge", "strategic", "apply"} {
		for _, version := range []string{"omitted", "current", "stale", "zero", "test", "matchingUID", "emptyUID", "wrongUID", "uncontended", "admissionReject"} {
			if version == "test" && method != "json" {
				continue
			}
			for _, dryRun := range []bool{false, true} {
				for _, force := range []bool{false, true} {
					if force && method != "apply" {
						continue
					}
					t.Run(fmt.Sprintf("%s/%s/dryRun=%v/force=%v", method, version, dryRun, force), func(t *testing.T) {
						var mu sync.Mutex
						writes := 0
						funcs := interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
							mu.Lock()
							defer mu.Unlock()
							writes++
							writeOptions := (&client.UpdateOptions{}).ApplyOptions(opts).AsUpdateOptions()
							if writeOptions.FieldManager != "concurrency-test" || writeOptions.FieldValidation != metav1.FieldValidationStrict || slices.Contains(writeOptions.DryRun, metav1.DryRunAll) != dryRun {
								return fmt.Errorf("write options changed: %#v", writeOptions)
							}
							if writes == 1 && version != "uncontended" {
								live := &helmv2.HelmRelease{}
								if err := c.Get(ctx, client.ObjectKeyFromObject(obj), live); err != nil {
									return err
								}
								live.Spec.Values = &apiextv1.JSON{Raw: []byte(`{"retained":2,"numeric":1e1,"_internal":true}`)}
								live.Spec.Suspend = true
								live.Labels["apps.cozystack.io-concurrent"] = "kept"
								live.Labels["foreign.example/controller"] = "kept"
								live.Labels[fluxshard.ShardKeyLabel] = "concurrent-shard"
								live.Annotations = map[string]string{"foreign.example/controller": "kept", "apps.cozystack.io-concurrent": "kept"}
								live.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: "owner", UID: "owner"}}
								live.Finalizers = []string{"finalizers.fluxcd.io", metav1.FinalizerDeleteDependents}
								if err := c.Update(ctx, live); err != nil {
									return err
								}
								if err := c.Delete(ctx, live); err != nil {
									return err
								}
							}
							// The fake client skips resourceVersion validation for dry-run writes.
							live := &helmv2.HelmRelease{}
							if err := c.Get(ctx, client.ObjectKeyFromObject(obj), live); err != nil {
								return err
							}
							if obj.GetResourceVersion() != live.ResourceVersion || obj.GetUID() != live.UID {
								return apierrors.NewConflict(helmv2.GroupVersion.WithResource("helmreleases").GroupResource(), obj.GetName(), fmt.Errorf("object changed"))
							}
							return c.Update(ctx, obj, opts...)
						}}
						probe := &updateAdmissionProbe{Handler: admission.NewHandler(admission.Update), dryRun: dryRun, rejectFresh: version == "admissionReject"}
						ts, backing := startAppsServerWithInterceptors(t, &config.ResourceConfig{Resources: []config.Resource{redisConfig}}, funcs, probe)
						ctx := context.Background()
						hr := &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Name: "redis-app", Namespace: "tenant-a", UID: "original", Labels: map[string]string{
							"apps.cozystack.io/application.kind": "Redis", "apps.cozystack.io/application.group": "apps.cozystack.io", "apps.cozystack.io/application.name": "app",
						}}, Spec: helmv2.HelmReleaseSpec{Values: &apiextv1.JSON{Raw: []byte(`{"retained":1,"numeric":10.0}`)}}}
						if err := backing.Create(ctx, hr); err != nil {
							t.Fatal(err)
						}
						url := ts.URL + "/apis/apps.cozystack.io/v1alpha1/namespaces/tenant-a/redises/app"
						metadata := map[string]any{"name": "app", "namespace": "tenant-a"}
						switch version {
						case "current", "test", "uncontended":
							metadata["resourceVersion"] = hr.ResourceVersion
						case "stale":
							metadata["resourceVersion"] = "999"
						case "zero":
							metadata["resourceVersion"] = "0"
						case "matchingUID":
							metadata["uid"] = "original"
						case "emptyUID":
							metadata["uid"] = ""
						case "wrongUID":
							metadata["uid"] = "different"
						}
						payload := map[string]any{"apiVersion": "apps.cozystack.io/v1alpha1", "kind": "Redis", "metadata": metadata, "spec": map[string]any{"requested": true}}
						verb, contentType := http.MethodPatch, "application/merge-patch+json"
						var body []byte
						switch method {
						case "put":
							verb, contentType = http.MethodPut, "application/json"
						case "strategic":
							contentType = "application/strategic-merge-patch+json"
						case "apply":
							contentType = "application/apply-patch+yaml"
						case "json":
							contentType = "application/json-patch+json"
							ops := []map[string]any{{"op": "add", "path": "/spec/requested", "value": true}}
							if rv, ok := metadata["resourceVersion"]; ok {
								op := "add"
								if version == "test" {
									op = "test"
								}
								ops = append(ops, map[string]any{"op": op, "path": "/metadata/resourceVersion", "value": rv})
							}
							if uid, ok := metadata["uid"]; ok {
								ops = append(ops, map[string]any{"op": "add", "path": "/metadata/uid", "value": uid})
							}
							body, _ = json.Marshal(ops)
						}
						if body == nil {
							body, _ = json.Marshal(payload)
						}
						url += "?fieldManager=concurrency-test&fieldValidation=Strict"
						if dryRun {
							url += "&dryRun=All"
						}
						if method == "apply" {
							url += fmt.Sprintf("&force=%v", force)
						}
						req, err := http.NewRequestWithContext(ctx, verb, url, bytes.NewReader(body))
						if err != nil {
							t.Fatal(err)
						}
						req.Header.Set("Content-Type", contentType)
						resp, err := http.DefaultClient.Do(req)
						if err != nil {
							t.Fatal(err)
						}
						response, err := io.ReadAll(resp.Body)
						if closeErr := resp.Body.Close(); closeErr != nil {
							t.Fatal(closeErr)
						}
						if err != nil {
							t.Fatal(err)
						}
						want := http.StatusOK
						if version == "current" || version == "stale" {
							want = http.StatusConflict
						}
						if version == "test" {
							want = http.StatusUnprocessableEntity
						}
						if version == "wrongUID" {
							want = http.StatusUnprocessableEntity
							if method == "put" {
								want = http.StatusConflict
							}
						}
						if version == "admissionReject" {
							want = http.StatusForbidden
						}
						if resp.StatusCode != want {
							t.Fatalf("HTTP %d, want %d: %s", resp.StatusCode, want, response)
						}
						mutatedVersions, validatedVersions := probe.versions()
						if want == http.StatusOK || want == http.StatusForbidden {
							wantCalls := 2
							if version == "uncontended" {
								wantCalls = 1
							}
							if len(mutatedVersions) != wantCalls || len(validatedVersions) != wantCalls {
								t.Fatalf("admission mutations=%v validations=%v, want %d calls", mutatedVersions, validatedVersions, wantCalls)
							}
							for i := range mutatedVersions {
								if mutatedVersions[i] != validatedVersions[i] {
									t.Fatalf("admission snapshot mismatch: mutations=%v validations=%v", mutatedVersions, validatedVersions)
								}
							}
							if wantCalls == 2 && mutatedVersions[0] == mutatedVersions[1] {
								t.Fatalf("admission used stale snapshot: %v", mutatedVersions)
							}
						}
						if want == http.StatusOK {
							var typed struct {
								Metadata metav1.ObjectMeta `json:"metadata"`
								Spec     struct {
									Numeric   int64 `json:"numeric"`
									Requested bool  `json:"requested"`
								} `json:"spec"`
							}
							if err := json.Unmarshal(response, &typed); err != nil {
								t.Fatalf("typed readback: %v; response=%s", err, response)
							}
							if !typed.Spec.Requested || typed.Metadata.Annotations["admitted-version"] != mutatedVersions[len(mutatedVersions)-1] {
								t.Fatalf("proposed update missing from response: %s", response)
							}
							if method != "put" && (typed.Spec.Numeric != 10 || bytes.Contains(response, []byte("_internal"))) {
								t.Fatalf("public projection changed: %s", response)
							}
						}
						live := &helmv2.HelmRelease{}
						if err := backing.Get(ctx, client.ObjectKeyFromObject(hr), live); err != nil {
							t.Fatal(err)
						}
						if dryRun && (live.Annotations["apps.cozystack.io-admitted-version"] != "" || len(live.ManagedFields) != 0) {
							t.Fatalf("dry-run admission or field management persisted: %#v", live.ObjectMeta)
						}
						if want == http.StatusOK && !dryRun && live.Annotations["apps.cozystack.io-admitted-version"] != mutatedVersions[len(mutatedVersions)-1] {
							t.Fatalf("persisted stale admission output: annotations=%v versions=%v", live.Annotations, mutatedVersions)
						}
						mu.Lock()
						attempts := writes
						mu.Unlock()
						if want == http.StatusForbidden && attempts != 1 {
							t.Fatalf("denied fresh admission reached backend: attempts=%d", attempts)
						}
						if version == "stale" || version == "wrongUID" {
							if attempts != 0 {
								t.Fatalf("failed identity constraint reached backend: %d", attempts)
							}
							return
						}
						if version == "uncontended" {
							if attempts != 1 {
								t.Fatalf("uncontended write attempts=%d", attempts)
							}
							if bytes.Contains(live.Spec.Values.Raw, []byte("requested")) == dryRun {
								t.Fatalf("dryRun=%v persisted=%s", dryRun, live.Spec.Values.Raw)
							}
							return
						}
						if live.Labels["foreign.example/controller"] != "kept" || live.Annotations["foreign.example/controller"] != "kept" || live.Labels[fluxshard.ShardKeyLabel] != "concurrent-shard" {
							t.Fatalf("concurrent controller metadata lost: %#v", live.ObjectMeta)
						}
						if len(live.OwnerReferences) != 1 || live.OwnerReferences[0].UID != "owner" || live.DeletionTimestamp == nil {
							t.Fatalf("concurrent lifecycle metadata lost: %#v", live.ObjectMeta)
						}
						wantFinalizers := 2
						if method == "put" && want == http.StatusOK && !dryRun {
							wantFinalizers = 1
						}
						if !live.Spec.Suspend || len(live.Finalizers) != wantFinalizers || !slices.Contains(live.Finalizers, "finalizers.fluxcd.io") || slices.Contains(live.Finalizers, metav1.FinalizerDeleteDependents) != (wantFinalizers == 2) {
							t.Fatalf("concurrent suspension/finalizers lost: %#v", live)
						}
						if want != http.StatusOK || dryRun {
							if bytes.Contains(live.Spec.Values.Raw, []byte("requested")) {
								t.Fatalf("failed/dry-run request persisted: %s", live.Spec.Values.Raw)
							}
							return
						}
						if attempts != 2 {
							t.Fatalf("write attempts=%d, want 2", attempts)
						}
						if !bytes.Contains(live.Spec.Values.Raw, []byte(`"requested":true`)) {
							t.Fatalf("request lost: %s", live.Spec.Values.Raw)
						}
						if method != "put" && (!bytes.Contains(live.Spec.Values.Raw, []byte(`"retained":2`)) || live.Labels["apps.cozystack.io-concurrent"] != "kept" || live.Annotations["apps.cozystack.io-concurrent"] != "kept") {
							t.Fatalf("patch lost concurrent fields: values=%s labels=%v", live.Spec.Values.Raw, live.Labels)
						}
					})
				}
			}
		}
	}
}
