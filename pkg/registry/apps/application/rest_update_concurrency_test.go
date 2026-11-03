package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
)

type concurrencyObjectInfo struct {
	preconditions *metav1.Preconditions
	transform     func(context.Context, runtime.Object) (runtime.Object, error)
}

func (i concurrencyObjectInfo) Preconditions() *metav1.Preconditions { return i.preconditions }
func (i concurrencyObjectInfo) UpdatedObject(ctx context.Context, old runtime.Object) (runtime.Object, error) {
	return i.transform(ctx, old)
}

func TestUpdatePreconditions(t *testing.T) {
	uid, emptyUID := types.UID("original"), types.UID("")
	rv, emptyRV, differentRV := "7", "", "07"
	differentUID := types.UID("different")
	for _, tc := range []struct {
		name          string
		preconditions *metav1.Preconditions
		missing       bool
		conflict      bool
		retry         bool
	}{
		{name: "none"},
		{name: "matching", preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}},
		{name: "RV changed after conflict", preconditions: &metav1.Preconditions{ResourceVersion: &rv}, retry: true, conflict: true},
		{name: "empty UID", preconditions: &metav1.Preconditions{UID: &emptyUID}, conflict: true},
		{name: "empty RV", preconditions: &metav1.Preconditions{ResourceVersion: &emptyRV}, conflict: true},
		{name: "missing with UID", preconditions: &metav1.Preconditions{UID: &uid}, missing: true, conflict: true},
		{name: "missing empty UID", preconditions: &metav1.Preconditions{UID: &emptyUID}, missing: true},
		{name: "different UID", preconditions: &metav1.Preconditions{UID: &differentUID}, conflict: true},
		{name: "exact RV string", preconditions: &metav1.Preconditions{ResourceVersion: &differentRV}, conflict: true},
		{name: "missing with RV", preconditions: &metav1.Preconditions{ResourceVersion: &rv}, missing: true, conflict: true},
		{name: "missing empty RV", preconditions: &metav1.Preconditions{ResourceVersion: &emptyRV}, missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			existing := optionsTestHelmRelease()
			existing.UID, existing.ResourceVersion = uid, rv
			objects := []client.Object{existing}
			if tc.missing {
				objects = nil
			}
			writes, callbacks := 0, 0
			r := newOptionsTestREST(t, interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					writes++
					if tc.retry && writes == 1 {
						live := &helmv2.HelmRelease{}
						if err := c.Get(ctx, client.ObjectKeyFromObject(obj), live); err != nil {
							return err
						}
						live.Spec.Suspend = true
						if err := c.Update(ctx, live); err != nil {
							return err
						}
					}
					return c.Update(ctx, obj, opts...)
				},
			}, objects...)
			info := concurrencyObjectInfo{preconditions: tc.preconditions, transform: func(context.Context, runtime.Object) (runtime.Object, error) {
				callbacks++
				return optionsTestApplication(), nil
			}}
			ctx := request.WithNamespace(context.Background(), optionsTestNamespace)
			_, _, err := r.Update(ctx, "example", info, nil, nil, tc.missing, &metav1.UpdateOptions{})
			if tc.conflict {
				wantCalls := 0
				if tc.retry {
					wantCalls = 1
				}
				if !apierrors.IsConflict(err) || writes != wantCalls || callbacks != wantCalls {
					t.Fatalf("err=%v writes=%d callbacks=%d, want %d", err, writes, callbacks, wantCalls)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if tc.retry {
				live := &helmv2.HelmRelease{}
				if err := r.c.Get(ctx, client.ObjectKeyFromObject(existing), live); err != nil {
					t.Fatal(err)
				}
				if live.ResourceVersion != "8" || !live.Spec.Suspend {
					t.Fatalf("concurrent write changed: %#v", live)
				}
			}
		})
	}
}

func TestUpdateOutputIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, rv          string
		uid               types.UID
		conflict, invalid bool
	}{
		{name: "omitted"},
		{name: "numeric zero", rv: "0"},
		{name: "current", rv: "7", uid: "original"},
		{name: "stale", rv: "6", conflict: true},
		{name: "changed UID", uid: "different", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			existing := optionsTestHelmRelease()
			existing.UID, existing.ResourceVersion = "original", "7"
			r := newOptionsTestREST(t, interceptor.Funcs{}, existing)
			app := optionsTestApplication()
			app.UID, app.ResourceVersion = tc.uid, tc.rv
			ctx := request.WithNamespace(context.Background(), optionsTestNamespace)
			got, _, err := r.Update(ctx, "example", concurrencyObjectInfo{transform: func(context.Context, runtime.Object) (runtime.Object, error) { return app, nil }}, nil, nil, false, &metav1.UpdateOptions{})
			if tc.conflict {
				if !apierrors.IsConflict(err) {
					t.Fatalf("expected conflict: %v", err)
				}
				return
			}
			if tc.invalid {
				if !apierrors.IsInvalid(err) {
					t.Fatalf("expected invalid UID: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.(*appsv1alpha1.Application).UID != "original" {
				t.Fatalf("identity lost: %#v", got)
			}
		})
	}
}

func TestUpdateRebuildsAttempt(t *testing.T) {
	for _, beforeSecondRead := range []bool{false, true} {
		for _, explicitRV := range []bool{false, true} {
			t.Run(fmt.Sprintf("secondRead=%v/explicitRV=%v", beforeSecondRead, explicitRV), func(t *testing.T) {
				existing := optionsTestHelmRelease()
				existing.UID, existing.ResourceVersion = "original", "7"
				existing.Spec.Values = &apiextv1.JSON{Raw: []byte(`{"counter":1}`)}
				reads, writes, callbacks, admissions := 0, 0, 0, 0
				mutate := func(ctx context.Context, c client.WithWatch) error {
					live := &helmv2.HelmRelease{}
					if err := c.Get(ctx, client.ObjectKeyFromObject(existing), live); err != nil {
						return err
					}
					live.Spec.Values = &apiextv1.JSON{Raw: []byte(`{"counter":2}`)}
					live.Spec.Suspend = true
					live.Labels[LabelPrefix+"concurrent"] = "kept"
					live.Labels["foreign"] = "kept"
					return c.Update(ctx, live)
				}
				r := newOptionsTestREST(t, interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						reads++
						if beforeSecondRead && reads == 2 {
							if err := mutate(ctx, c); err != nil {
								return err
							}
						}
						return c.Get(ctx, key, obj, opts...)
					},
					Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						writes++
						if !beforeSecondRead && writes == 1 {
							if err := mutate(ctx, c); err != nil {
								return err
							}
						}
						return c.Update(ctx, obj, opts...)
					},
				}, existing)
				info := concurrencyObjectInfo{transform: func(_ context.Context, old runtime.Object) (runtime.Object, error) {
					callbacks++
					app := old.(*appsv1alpha1.Application).DeepCopy()
					if explicitRV {
						app.ResourceVersion = "7"
					}
					var values map[string]any
					if err := json.Unmarshal(app.Spec.Raw, &values); err != nil {
						return nil, err
					}
					values["requested"] = true
					app.Spec.Raw, _ = json.Marshal(values)
					old.(*appsv1alpha1.Application).Spec.Raw = []byte(`{"counter":999}`)
					return app, nil
				}}
				ctx := request.WithNamespace(context.Background(), optionsTestNamespace)
				_, _, err := r.Update(ctx, "example", info, nil, func(_ context.Context, _, old runtime.Object) error {
					admissions++
					if bytes.Contains(old.(*appsv1alpha1.Application).Spec.Raw, []byte("999")) {
						t.Fatal("callback mutated admission snapshot")
					}
					return nil
				}, false, &metav1.UpdateOptions{})
				wantWrites := 2
				if beforeSecondRead {
					wantWrites = 1
				}
				if explicitRV {
					wantWrites--
					if !apierrors.IsConflict(err) || writes != wantWrites {
						t.Fatalf("stale RV: err=%v writes=%d, want %d", err, writes, wantWrites)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if writes != wantWrites {
					t.Fatalf("writes=%d, want %d", writes, wantWrites)
				}
				live := &helmv2.HelmRelease{}
				if err := r.c.Get(ctx, client.ObjectKeyFromObject(existing), live); err != nil {
					t.Fatal(err)
				}
				if callbacks < 2 || admissions < 2 || !live.Spec.Suspend || live.Labels[LabelPrefix+"concurrent"] != "kept" || live.Labels["foreign"] != "kept" {
					t.Fatalf("callbacks=%d admissions=%d live=%#v", callbacks, admissions, live)
				}
				app, err := r.ConvertHelmReleaseToApplication(ctx, live)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(app.Spec.Raw, []byte(`"counter":2`)) || !bytes.Contains(app.Spec.Raw, []byte(`"requested":true`)) {
					t.Fatalf("lost concurrent spec: %v", app.Spec)
				}
			})
		}
	}
}

func TestUpdateStatusConflict(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			existing := optionsTestHelmRelease()
			existing.UID, existing.ResourceVersion = "original", "7"
			writes, callbacks, admissions := 0, 0, 0
			r := newOptionsTestREST(t, interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				writes++
				if writes == 1 {
					live := &helmv2.HelmRelease{}
					if err := c.Get(ctx, client.ObjectKeyFromObject(existing), live); err != nil {
						return err
					}
					live.Status.LastAttemptedRevision = "new-status"
					if err := c.Status().Update(ctx, live); err != nil {
						return err
					}
				}
				return c.Update(ctx, obj, opts...)
			}}, existing)
			info := concurrencyObjectInfo{transform: func(_ context.Context, old runtime.Object) (runtime.Object, error) {
				callbacks++
				app := old.(*appsv1alpha1.Application).DeepCopy()
				if explicit {
					app.ResourceVersion = "7"
				}
				return app, nil
			}}
			ctx := request.WithNamespace(context.Background(), optionsTestNamespace)
			got, _, err := r.Update(ctx, "example", info, nil, func(_ context.Context, _, old runtime.Object) error {
				admissions++
				if admissions == 2 && old.(*appsv1alpha1.Application).Status.Version != "new-status" {
					t.Fatal("stale admission status")
				}
				return nil
			}, false, &metav1.UpdateOptions{})
			if explicit {
				if !apierrors.IsConflict(err) || writes != 1 {
					t.Fatalf("err=%v writes=%d", err, writes)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if callbacks != 2 || admissions != 2 || got.(*appsv1alpha1.Application).Status.Version != "new-status" {
				t.Fatalf("callbacks=%d admissions=%d result=%#v", callbacks, admissions, got)
			}
		})
	}
}

func TestUpdateConflictExhaustionAndCallbackErrors(t *testing.T) {
	for _, failure := range []string{"backend", "callback", "admission"} {
		t.Run(failure, func(t *testing.T) {
			conflict := apierrors.NewConflict(helmv2.GroupVersion.WithResource("helmreleases").GroupResource(), "postgresql-example", fmt.Errorf("conflict"))
			writes, callbacks, admissions := 0, 0, 0
			r := newOptionsTestREST(t, interceptor.Funcs{Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
				writes++
				return conflict
			}}, optionsTestHelmRelease())
			info := concurrencyObjectInfo{transform: func(context.Context, runtime.Object) (runtime.Object, error) {
				callbacks++
				if failure == "callback" {
					return nil, conflict
				}
				return optionsTestApplication(), nil
			}}
			ctx := request.WithNamespace(context.Background(), optionsTestNamespace)
			_, _, err := r.Update(ctx, "example", info, nil, func(context.Context, runtime.Object, runtime.Object) error {
				admissions++
				if failure == "admission" {
					return conflict
				}
				return nil
			}, false, &metav1.UpdateOptions{})
			if !apierrors.IsConflict(err) {
				t.Fatalf("expected conflict: %v", err)
			}
			if failure == "backend" {
				if writes != retry.DefaultBackoff.Steps || callbacks != writes || admissions != writes {
					t.Fatalf("writes=%d callbacks=%d admissions=%d", writes, callbacks, admissions)
				}
			} else if writes != 0 || callbacks != 1 {
				t.Fatalf("non-backend conflict retried: writes=%d callbacks=%d", writes, callbacks)
			}
		})
	}
}

func TestUpdateUsesDirectReader(t *testing.T) {
	existing := optionsTestHelmRelease()
	existing.UID, existing.ResourceVersion = "original", "7"
	r := newOptionsTestREST(t, interceptor.Funcs{}, existing)
	direct := r.w
	cached := interceptor.NewClient(direct, interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
		t.Fatal("update consulted cached reader")
		return nil
	}})
	r.c = cached
	app := optionsTestApplication()
	app.ResourceVersion = "7"
	ctx := request.WithNamespace(context.Background(), optionsTestNamespace)
	if _, _, err := r.Update(ctx, "example", rest.DefaultUpdatedObjectInfo(app), nil, nil, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateRecreation(t *testing.T) {
	for _, betweenReads := range []bool{false, true} {
		for _, constrained := range []bool{false, true} {
			t.Run(fmt.Sprintf("betweenReads=%v/constrained=%v", betweenReads, constrained), func(t *testing.T) {
				existing := optionsTestHelmRelease()
				existing.UID, existing.ResourceVersion = "original", "7"
				existing.Spec.Values = &apiextv1.JSON{Raw: []byte(`{"incarnation":"original"}`)}
				reads, writes, callbacks := 0, 0, 0
				recreate := func(ctx context.Context, c client.WithWatch) error {
					if err := c.Delete(ctx, existing); err != nil {
						return err
					}
					replacement := existing.DeepCopy()
					replacement.UID, replacement.ResourceVersion = "replacement", ""
					replacement.Spec.Values = &apiextv1.JSON{Raw: []byte(`{"incarnation":"replacement"}`)}
					return c.Create(ctx, replacement)
				}
				r := newOptionsTestREST(t, interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						reads++
						if betweenReads && reads == 2 {
							if err := recreate(ctx, c); err != nil {
								return err
							}
						}
						return c.Get(ctx, key, obj, opts...)
					},
					Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						writes++
						if !betweenReads && writes == 1 {
							if err := recreate(ctx, c); err != nil {
								return err
							}
						}
						return c.Update(ctx, obj, opts...)
					},
				}, existing)
				info := concurrencyObjectInfo{transform: func(_ context.Context, old runtime.Object) (runtime.Object, error) {
					callbacks++
					return old.DeepCopyObject(), nil
				}}
				if constrained {
					uid := existing.UID
					info.preconditions = &metav1.Preconditions{UID: &uid}
				}
				ctx := request.WithNamespace(context.Background(), optionsTestNamespace)
				got, _, err := r.Update(ctx, "example", info, nil, nil, false, &metav1.UpdateOptions{})
				if constrained {
					if !apierrors.IsConflict(err) || callbacks != 1 {
						t.Fatalf("err=%v callbacks=%d", err, callbacks)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if callbacks != 2 || got.(*appsv1alpha1.Application).UID != "replacement" || !bytes.Contains(got.(*appsv1alpha1.Application).Spec.Raw, []byte("replacement")) {
					t.Fatalf("callbacks=%d result=%#v", callbacks, got)
				}
			})
		}
	}
}

func TestUpdateValidationCannotChangeWriteIdentity(t *testing.T) {
	existing := optionsTestHelmRelease()
	existing.UID, existing.ResourceVersion = "original", "7"
	r := newOptionsTestREST(t, interceptor.Funcs{}, existing)
	ctx := request.WithNamespace(context.Background(), optionsTestNamespace)
	got, _, err := r.Update(ctx, "example", rest.DefaultUpdatedObjectInfo(optionsTestApplication()), nil, func(_ context.Context, obj, old runtime.Object) error {
		obj.(*appsv1alpha1.Application).UID = "validator-mutation"
		old.(*appsv1alpha1.Application).UID = "validator-mutation"
		return nil
	}, false, &metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.(*appsv1alpha1.Application).UID != "original" {
		t.Fatalf("validator changed persisted identity: %#v", got)
	}
}

func TestUpdateMissingWithoutCreateRemainsNotFound(t *testing.T) {
	r := newOptionsTestREST(t, interceptor.Funcs{})
	app := optionsTestApplication()
	app.UID = "missing"
	ctx := request.WithNamespace(context.Background(), optionsTestNamespace)
	if _, _, err := r.Update(ctx, "example", rest.DefaultUpdatedObjectInfo(app), nil, nil, false, &metav1.UpdateOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected NotFound without create-on-update, got %v", err)
	}
}

func TestUpdateDiscardsEqualPublicSnapshots(t *testing.T) {
	existing := optionsTestHelmRelease()
	existing.UID, existing.ResourceVersion = "original", "7"
	existing.Spec.Values = &apiextv1.JSON{Raw: []byte(`{"counter":10.0}`)}
	reads, writes := 0, 0
	var publicSnapshots [][]byte
	r := newOptionsTestREST(t, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			reads++
			if reads == 2 {
				live := &helmv2.HelmRelease{}
				if err := c.Get(ctx, key, live); err != nil {
					return err
				}
				live.Spec.Values = &apiextv1.JSON{Raw: []byte(`{"counter":1e1}`)}
				if err := c.Update(ctx, live); err != nil {
					return err
				}
			}
			return c.Get(ctx, key, obj, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			writes++
			return c.Update(ctx, obj, opts...)
		},
	}, existing)
	info := concurrencyObjectInfo{transform: func(_ context.Context, old runtime.Object) (runtime.Object, error) {
		app := old.(*appsv1alpha1.Application)
		publicSnapshots = append(publicSnapshots, bytes.Clone(app.Spec.Raw))
		return app.DeepCopy(), nil
	}}
	ctx := request.WithNamespace(context.Background(), optionsTestNamespace)
	if _, _, err := r.Update(ctx, "example", info, nil, nil, false, &metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(publicSnapshots) != 2 || !bytes.Equal(publicSnapshots[0], publicSnapshots[1]) || writes != 1 {
		t.Fatalf("public snapshots=%q writes=%d", publicSnapshots, writes)
	}
}

var _ rest.UpdatedObjectInfo = concurrencyObjectInfo{}
