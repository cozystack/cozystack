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
	"errors"
	"testing"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestWorkloadMemoryAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, chart, old, next string
		upsert, reject         bool
	}{
		{"new bare bytes", "tcp-balancer", "", `{"resources":{"memory":8}}`, false, true},
		{"upsert respects floor", "tcp-balancer", "", `{"resources":{"memory":"8"}}`, true, true},
		{"new zero", "tcp-balancer", "", `{"resources":{"memory":0}}`, false, true},
		{"minimum", "tcp-balancer", "", `{"resources":{"memory":"1Mi"}}`, false, false},
		{"bytes above minimum", "tcp-balancer", "", `{"resources":{"memory":1048576}}`, false, false},
		{"unchanged scaled to zero", "tcp-balancer", `{"resources":{"memory":8},"replicas":0}`, `{"resources":{"memory":8},"replicas":0,"other":true}`, false, false},
		{"same quantity spelling", "tcp-balancer", `{"resources":{"memory":8}}`, `{"resources":{"memory":"8"}}`, false, false},
		{"changed zero", "tcp-balancer", `{"resources":{"memory":8}}`, `{"resources":{"memory":0}}`, false, true},
		{"repaired", "tcp-balancer", `{"resources":{"memory":8}}`, `{"resources":{"memory":"8Gi"}}`, false, false},
		{"back to preset", "tcp-balancer", `{"resources":{"memory":8}}`, `{"resourcesPreset":"t1.nano"}`, false, false},
		{"nested component", "kubernetes", "", `{"controlPlane":{"apiServer":{"resources":{"memory":"1023Ki"}}}}`, false, true},
		{"quota zero", "tenant", "", `{"resourceQuotas":{"memory":0}}`, false, false},
		{"free form config", "nats", "", `{"config":{"merge":{"resourcesPreset":"example","resources":{"memory":8}}}}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &appsv1alpha1.Application{
				TypeMeta:   metav1.TypeMeta{Kind: kubernetesKind, APIVersion: "apps.cozystack.io/v1alpha1"},
				ObjectMeta: metav1.ObjectMeta{Name: "good-name", Namespace: "tenant-foo"},
			}
			app.Spec = &apiextv1.JSON{Raw: []byte(tc.next)}
			var stored []client.Object
			if tc.old != "" {
				stored = []client.Object{&helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Name: "kubernetes-" + app.Name, Namespace: app.Namespace, Labels: map[string]string{
					ApplicationKindLabel: kubernetesKind, ApplicationGroupLabel: appsv1alpha1.GroupName, ApplicationNameLabel: app.Name,
				}}, Spec: helmv2.HelmReleaseSpec{Values: &apiextv1.JSON{Raw: []byte(tc.old)}}}}
			}
			r := newKubernetesWarnREST(t, stored...)
			r.specSchema = chartSpecSchema(t, tc.chart)
			ctx := request.WithNamespace(context.Background(), app.Namespace)
			var err error
			if tc.old != "" || tc.upsert {
				_, _, err = r.Update(ctx, app.Name, newDefaultUpdatedObjectInfo(app), nil, nil, tc.upsert, &metav1.UpdateOptions{})
			} else {
				_, err = r.Create(ctx, app, nil, &metav1.CreateOptions{})
			}
			if tc.reject && !apierrors.IsInvalid(err) || !tc.reject && err != nil {
				t.Fatalf("reject=%v, got %v", tc.reject, err)
			}
			if tc.reject {
				list := &helmv2.HelmReleaseList{}
				if err := r.c.List(ctx, list); err != nil {
					t.Fatal(err)
				}
				if len(list.Items) != len(stored) || len(stored) > 0 && string(list.Items[0].Spec.Values.Raw) != tc.old {
					t.Fatal("a rejected request changed storage")
				}
			}
		})
	}
}

// A caller that read the app before another writer fixed its memory still
// carries the old value. It must not put that value back, whether the fix lands
// before Update reads the live release or between that read and the write.
func TestWorkloadMemoryRecheckedAgainstLiveRelease(t *testing.T) {
	const stale, fixed = `{"resources":{"memory":8}}`, `{"resources":{"memory":"512Mi"}}`
	conflict := func(name string) error {
		return apierrors.NewConflict(schema.GroupResource{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"}, name, errors.New("simulated concurrent write"))
	}
	setValues := func(ctx context.Context, c client.WithWatch, key client.ObjectKey, values string) error {
		live := &helmv2.HelmRelease{}
		if err := c.Get(ctx, key, live); err != nil {
			return err
		}
		live.Spec.Values = &apiextv1.JSON{Raw: []byte(values)}
		return c.Update(ctx, live)
	}
	for _, tc := range []struct {
		name, next, want string
		reject           bool
		funcs            func() interceptor.Funcs
	}{
		{"fixed after the first read", `{"resources":{"memory":8},"other":true}`, fixed, true, func() interceptor.Funcs {
			gets := 0
			return interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				err := c.Get(ctx, key, obj, opts...)
				if _, ok := obj.(*helmv2.HelmRelease); ok && err == nil {
					if gets++; gets == 1 {
						return setValues(ctx, c, key, fixed)
					}
				}
				return err
			}}
		}},
		{"fixed between the live read and the write", `{"resources":{"memory":8},"other":true}`, fixed, true, func() interceptor.Funcs {
			writes := 0
			return interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if _, ok := obj.(*helmv2.HelmRelease); ok {
					if writes++; writes == 1 {
						if err := setValues(ctx, c, client.ObjectKeyFromObject(obj), fixed); err != nil {
							return err
						}
						return conflict(obj.GetName())
					}
				}
				return c.Update(ctx, obj, opts...)
			}}
		}},
		{"unrelated edit after a conflict", `{"resources":{"memory":8},"other":true}`, `{"resources":{"memory":8},"other":true}`, false, func() interceptor.Funcs {
			writes := 0
			return interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if _, ok := obj.(*helmv2.HelmRelease); ok {
					if writes++; writes == 1 {
						return conflict(obj.GetName())
					}
				}
				return c.Update(ctx, obj, opts...)
			}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &appsv1alpha1.Application{
				TypeMeta:   metav1.TypeMeta{Kind: kubernetesKind, APIVersion: "apps.cozystack.io/v1alpha1"},
				ObjectMeta: metav1.ObjectMeta{Name: "good-name", Namespace: "tenant-foo"},
				Spec:       &apiextv1.JSON{Raw: []byte(tc.next)},
			}
			stored := &helmv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Name: "kubernetes-" + app.Name, Namespace: app.Namespace, Labels: map[string]string{
				ApplicationKindLabel: kubernetesKind, ApplicationGroupLabel: appsv1alpha1.GroupName, ApplicationNameLabel: app.Name,
			}}, Spec: helmv2.HelmReleaseSpec{Values: &apiextv1.JSON{Raw: []byte(stale)}}}
			r := newKubernetesWarnREST(t, stored)
			r.specSchema = chartSpecSchema(t, "tcp-balancer")
			base := r.w
			intercepted := interceptor.NewClient(base, tc.funcs())
			r.c = intercepted
			r.w = intercepted
			ctx := request.WithNamespace(context.Background(), app.Namespace)
			_, _, err := r.Update(ctx, app.Name, newDefaultUpdatedObjectInfo(app), nil, nil, false, &metav1.UpdateOptions{})
			if tc.reject && !apierrors.IsInvalid(err) || !tc.reject && err != nil {
				t.Fatalf("reject=%v, got %v", tc.reject, err)
			}
			got := &helmv2.HelmRelease{}
			if err := base.Get(ctx, client.ObjectKeyFromObject(stored), got); err != nil {
				t.Fatal(err)
			}
			if string(got.Spec.Values.Raw) != tc.want {
				t.Fatalf("stored values %s, want %s", got.Spec.Values.Raw, tc.want)
			}
		})
	}
}
