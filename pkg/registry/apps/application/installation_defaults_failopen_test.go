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

package application

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/warning"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestInstallationDefaultsFailuresDoNotBlockCreation(t *testing.T) {
	for _, tc := range []struct {
		name, raw, schema string
		denied            bool
	}{
		{"read denied", `{}`, installationDefaultsSchema, true},
		{"invalid JSON", `{`, installationDefaultsSchema, false},
		{"invalid structure", `{"widgets":null}`, installationDefaultsSchema, false},
		{"reserved fields", `{"widgets":{"_cluster":{}}}`, installationDefaultsSchema, false},
		{"unknown field", `{"widgets":{"typo":true}}`, installationDefaultsSchema, false},
		{"no partial merge", `{"widgets":{"replicas":3,"typo":true}}`, installationDefaultsSchema, false},
		{"wrong type", `{"widgets":{"replicas":"wrong"}}`, installationDefaultsSchema, false},
		{"missing schema", `{"widgets":{"replicas":3}}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := defaultsREST(t, defaultsConfig(tc.raw))
			r = NewREST(r.c, r.w, r.c, &config.Resource{
				Application: config.ApplicationConfig{Kind: "Widget", Plural: "widgets", OpenAPISchema: tc.schema},
				Release:     config.ReleaseConfig{Prefix: "widget-"},
			})
			if tc.denied {
				r.defaultsReader = &failingDefaultsReader{Reader: r.c}
			}
			called := false
			_, err := r.Create(context.Background(), defaultsApp(`{"required":"user"}`), func(_ context.Context, obj runtime.Object) error {
				called = true
				assertSpec(t, obj.(*appsv1alpha1.Application).Spec, `{"required":"user"}`)
				return nil
			}, &metav1.CreateOptions{})
			if err != nil || !called {
				t.Fatalf("create must continue without defaults: %v (admission=%v)", err, called)
			}
			hr := &helmv2.HelmRelease{}
			if err := r.c.Get(context.Background(), client.ObjectKey{Namespace: "tenant-test", Name: "widget-example"}, hr); err != nil {
				t.Fatal(err)
			}
			assertSpec(t, hr.Spec.Values, `{"required":"user"}`)
		})
	}
}

const skippedDefaultsWarning = "Installation defaults could not be applied; continuing without them"

type recordedWarnings []string

func (w *recordedWarnings) AddWarning(_, text string) { *w = append(*w, text) }

// A skipped default cannot be recovered after creation, so the client must be
// told at the request that skipped it, and a dry-run is where an operator looks.
func TestInstallationDefaultsWarnOnFallbackIncludingDryRun(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		denied    bool
	}{
		{"read denied", `{}`, true},
		{"invalid JSON", `{`, false},
		{"unknown field", `{"widgets":{"typo":true}}`, false},
	} {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s dryRun=%v", tc.name, dryRun), func(t *testing.T) {
				r := defaultsRESTWithSchema(t, installationDefaultsSchema, defaultsConfig(tc.raw))
				if tc.denied {
					r.defaultsReader = &failingDefaultsReader{Reader: r.c}
				}
				var got recordedWarnings
				ctx := warning.WithWarningRecorder(context.Background(), &got)
				opts := &metav1.CreateOptions{}
				if dryRun {
					opts.DryRun = []string{metav1.DryRunAll}
				}
				if _, err := r.Create(ctx, defaultsApp(`{"required":"user"}`), nil, opts); err != nil {
					t.Fatal(err)
				}
				if len(got) != 1 || got[0] != skippedDefaultsWarning {
					t.Fatalf("warnings = %q, want exactly %q", []string(got), skippedDefaultsWarning)
				}
			})
		}
	}
}

type blockingDefaultsReader struct{ client.Reader }

func (blockingDefaultsReader) Get(ctx context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	<-ctx.Done()
	return ctx.Err()
}

// A cache that has not synced blocks a read until its context ends. The create
// must not wait for the request to time out: it falls back like any read error.
func TestInstallationDefaultsReadIsBounded(t *testing.T) {
	r := defaultsRESTWithSchema(t, installationDefaultsSchema, defaultsConfig(`{"widgets":{"replicas":3}}`))
	r.defaultsReader = blockingDefaultsReader{Reader: r.c}
	r.defaultsReadLimit = 20 * time.Millisecond
	var got recordedWarnings
	ctx, cancel := context.WithTimeout(warning.WithWarningRecorder(context.Background(), &got), 5*time.Second)
	defer cancel()
	started := time.Now()
	if _, err := r.Create(ctx, defaultsApp(`{"required":"user"}`), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("create waited %s for the defaults read", elapsed)
	}
	if len(got) != 1 || got[0] != skippedDefaultsWarning {
		t.Fatalf("warnings = %q, want exactly %q", []string(got), skippedDefaultsWarning)
	}
}

// Applied defaults are normal operation: warning on every create would teach
// clients to ignore the warning that matters.
func TestInstallationDefaultsNoWarningWhenAppliedOrNotConfigured(t *testing.T) {
	for _, tc := range []struct {
		name    string
		objects []client.Object
	}{
		{"applied", []client.Object{defaultsConfig(`{"widgets":{"replicas":3}}`)}},
		{"no entry for the application", []client.Object{defaultsConfig(`{"otherwidgets":{"replicas":3}}`)}},
		{"no ConfigMap", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := defaultsRESTWithSchema(t, installationDefaultsSchema, tc.objects...)
			var got recordedWarnings
			ctx := warning.WithWarningRecorder(context.Background(), &got)
			if _, err := r.Create(ctx, defaultsApp(`{"required":"user"}`), nil, &metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Fatalf("unexpected warnings: %q", []string(got))
			}
		})
	}
}

func TestInstallationDefaultsFallbackKeepsOrdinaryRejections(t *testing.T) {
	t.Run("reserved user field", func(t *testing.T) {
		r := defaultsREST(t, defaultsConfig(`{`))
		_, err := r.Create(context.Background(), defaultsApp(`{"_cluster":{}}`), nil, &metav1.CreateOptions{})
		if !apierrors.IsBadRequest(err) {
			t.Fatalf("invalid user spec was accepted: %v", err)
		}
		list := &helmv2.HelmReleaseList{}
		if err := r.c.List(context.Background(), list); err != nil || len(list.Items) != 0 {
			t.Fatalf("unexpected write: %v %v", list.Items, err)
		}
	})
	t.Run("admission", func(t *testing.T) {
		r := defaultsREST(t, defaultsConfig(`{`))
		rejected := errors.New("admission rejected user request")
		_, err := r.Create(context.Background(), defaultsApp(`{}`), func(context.Context, runtime.Object) error { return rejected }, &metav1.CreateOptions{})
		if !errors.Is(err, rejected) {
			t.Fatalf("admission rejection lost: %v", err)
		}
		list := &helmv2.HelmReleaseList{}
		if err := r.c.List(context.Background(), list); err != nil || len(list.Items) != 0 {
			t.Fatalf("unexpected write: %v %v", list.Items, err)
		}
	})
	t.Run("quota", func(t *testing.T) {
		parent := tenantHelmRelease(t, "parent", "tenant-root", map[string]string{"cpu": "2"})
		r := newTenantREST(t, parent, defaultsConfig(`{`))
		r.defaultsReader = r.c
		_, err := r.Create(context.Background(), childApplication(t, "child", "tenant-root-parent", map[string]string{"cpu": "4"}), nil, &metav1.CreateOptions{})
		if !apierrors.IsInvalid(err) {
			t.Fatalf("quota rejection lost: %v", err)
		}
	})
}
