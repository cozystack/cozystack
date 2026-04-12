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
	"fmt"
	"strings"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/apis/apps/validation"
	"github.com/cozystack/cozystack/pkg/config"
)

func TestValidateNameFormat(t *testing.T) {
	tests := []struct {
		name      string
		kindName  string
		appName   string
		wantError bool
	}{
		// Non-tenant kinds follow DNS-1035 only — hyphens are allowed.
		{"non-tenant accepts hyphen", "MySQL", "my-db", false},
		{"non-tenant accepts double hyphen", "MySQL", "my--db", false},
		{"non-tenant rejects uppercase", "MySQL", "MyDB", true},

		// Tenant kind enforces alphanumeric-only — see
		// packages/apps/tenant/templates/_helpers.tpl for the reason.
		{"tenant accepts alphanumeric", "Tenant", "foo", false},
		{"tenant accepts digits", "Tenant", "foo123", false},
		{"tenant rejects single hyphen", "Tenant", "foo-bar", true},
		{"tenant rejects leading hyphen", "Tenant", "-foo", true},
		{"tenant rejects trailing hyphen", "Tenant", "foo-", true},
		{"tenant rejects uppercase", "Tenant", "Foo", true},
		{"tenant rejects underscore", "Tenant", "foo_bar", true},
		{"tenant rejects empty", "Tenant", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &REST{kindName: tt.kindName}

			errs := r.validateNameFormat(tt.appName)

			if tt.wantError && len(errs) == 0 {
				t.Errorf("expected error for name %q (kind=%q), got none", tt.appName, tt.kindName)
			}
			if !tt.wantError && len(errs) > 0 {
				t.Errorf("unexpected error for name %q (kind=%q): %v", tt.appName, tt.kindName, errs)
			}
		})
	}
}

// TestUpdate_ForceAllowCreate_RejectsTenantDashName pins the wiring from the
// Update → Create fall-through path. When a user runs `kubectl apply` and
// the object does not yet exist, Kubernetes routes the request through
// REST.Update with forceAllowCreate=true, which delegates to REST.Create
// (rest.go:452). Without this test, a future refactor could quietly reroute
// that delegation and bypass the tenant name check — unit tests of the
// pure r.validateNameFormat method would still pass while upsert-style
// kubectl apply regressed back to accepting tenant names with dashes.
func TestUpdate_ForceAllowCreate_RejectsTenantDashName(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("register helmv2 scheme: %v", err)
	}
	// Register the dynamic Tenant kind so the Application type round-trips
	// through the scheme the same way the real aggregated API server wires
	// it at startup.
	resourceCfg := &config.ResourceConfig{
		Resources: []config.Resource{
			{
				Application: config.ApplicationConfig{Kind: "Tenant"},
			},
		},
	}
	if err := appsv1alpha1.RegisterDynamicTypes(scheme, resourceCfg); err != nil {
		t.Fatalf("register dynamic Tenant type: %v", err)
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &REST{
		c: fakeClient,
		gvr: schema.GroupVersionResource{
			Group:    appsv1alpha1.GroupName,
			Version:  "v1alpha1",
			Resource: "tenants",
		},
		gvk: schema.GroupVersionKind{
			Group:   appsv1alpha1.GroupName,
			Version: "v1alpha1",
			Kind:    "Tenant",
		},
		kindName: "Tenant",
		releaseConfig: config.ReleaseConfig{
			Prefix: "tenant-",
		},
	}

	newApp := &appsv1alpha1.Application{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps.cozystack.io/v1alpha1",
			Kind:       "Tenant",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-bar",
			Namespace: "tenant-root",
		},
	}

	ctx := request.WithNamespace(context.Background(), "tenant-root")

	_, _, err := r.Update(
		ctx,
		"foo-bar",
		rest.DefaultUpdatedObjectInfo(newApp),
		nil,                      // createValidation
		nil,                      // updateValidation
		true,                     // forceAllowCreate → routes through Create on NotFound
		&metav1.UpdateOptions{},
	)
	if err == nil {
		t.Fatalf("expected Update to reject tenant name with dashes, got no error")
	}
	if !apierrors.IsInvalid(err) {
		t.Errorf("expected Invalid status error, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "tenant names must") {
		t.Errorf("expected tenant-specific error in %q", err.Error())
	}
}

// TestConvertHelmReleaseToApplication_TenantNamespaceKindGate pins the
// behavior that convertHelmReleaseToApplication fills Status.Namespace only
// when the kind is Tenant. This path is gated on r.kindName matching a
// specific literal — the test uses the validation.TenantKind constant so
// that if the source of truth for the tenant kind string is ever renamed,
// the gate and the constant drift together (or the test fails).
func TestConvertHelmReleaseToApplication_TenantNamespaceKindGate(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("register helmv2 scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("register corev1 scheme: %v", err)
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	hr := &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tenant-foo",
			Namespace: "tenant-root",
		},
	}

	t.Run("tenant kind fills Status.Namespace", func(t *testing.T) {
		r := &REST{
			c:        fakeClient,
			kindName: validation.TenantKind,
			releaseConfig: config.ReleaseConfig{
				Prefix: "tenant-",
			},
		}

		app, err := r.convertHelmReleaseToApplication(context.Background(), hr)
		if err != nil {
			t.Fatalf("convertHelmReleaseToApplication: %v", err)
		}
		if app.Status.Namespace == "" {
			t.Errorf("expected Status.Namespace to be populated for tenant kind, got empty")
		}
	})

	t.Run("non-tenant kind leaves Status.Namespace empty", func(t *testing.T) {
		r := &REST{
			c:        fakeClient,
			kindName: "MySQL",
			releaseConfig: config.ReleaseConfig{
				Prefix: "mysql-",
			},
		}

		app, err := r.convertHelmReleaseToApplication(context.Background(), hr)
		if err != nil {
			t.Fatalf("convertHelmReleaseToApplication: %v", err)
		}
		if app.Status.Namespace != "" {
			t.Errorf("expected Status.Namespace to be empty for non-tenant kind, got %q", app.Status.Namespace)
		}
	})
}

func TestValidateNameLength(t *testing.T) {
	tests := []struct {
		name      string
		kindName  string
		prefix    string
		appName   string
		wantError bool
	}{
		{
			name:      "short name passes",
			kindName:  "MySQL",
			prefix:    "mysql-",
			appName:   "mydb",
			wantError: false,
		},
		{
			name:      "at helm boundary passes",
			kindName:  "MySQL",
			prefix:    "mysql-",
			appName:   strings.Repeat("a", 53-len("mysql-")), // exactly 47 chars
			wantError: false,
		},
		{
			name:      "exceeding helm limit fails",
			kindName:  "MySQL",
			prefix:    "mysql-",
			appName:   strings.Repeat("a", 53-len("mysql-")+1), // 48 chars
			wantError: true,
		},
		{
			name:      "tenant within helm limit passes",
			kindName:  "Tenant",
			prefix:    "tenant-",
			appName:   strings.Repeat("a", 53-len("tenant-")), // 46 chars
			wantError: false,
		},
		{
			name:      "tenant exceeding helm limit fails",
			kindName:  "Tenant",
			prefix:    "tenant-",
			appName:   strings.Repeat("a", 53-len("tenant-")+1), // 47 chars
			wantError: true,
		},
		{
			name:      "prefix consuming all helm capacity returns config error",
			kindName:  "MySQL",
			prefix:    strings.Repeat("x", 53), // prefix == maxHelmReleaseName → helmMax = 0
			appName:   "a",
			wantError: true,
		},
		{
			name:      "prefix exceeding helm capacity returns config error",
			kindName:  "MySQL",
			prefix:    strings.Repeat("x", 60), // prefix > maxHelmReleaseName → helmMax < 0
			appName:   "a",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &REST{
				kindName: tt.kindName,
				releaseConfig: config.ReleaseConfig{
					Prefix: tt.prefix,
				},
			}

			errs := r.validateNameLength(tt.appName)

			if tt.wantError && len(errs) == 0 {
				t.Errorf("expected error for name %q (len=%d), got none", tt.appName, len(tt.appName))
			}
			if !tt.wantError && len(errs) > 0 {
				t.Errorf("unexpected error for name %q (len=%d): %v", tt.appName, len(tt.appName), errs)
			}
		})
	}
}

// TestValidateTenantNamespaceLength covers the check that the computed
// workload namespace for a Tenant application fits inside the 63-char
// DNS-1123 label limit. The namespace is formed by dash-joining the
// parent namespace with the tenant name, so deep nesting can exceed the
// limit even when each individual name passes the per-name Helm release
// length check.
//
// The "tenant-root" branches of computeTenantNamespace do not get a
// dedicated overflow case: that branch produces "tenant-<name>" (7 +
// len(name)), so for the computed result to exceed 63 chars the name
// would need to exceed 56 chars, which is already blocked by
// validateNameLength (max 46 for the "tenant-" prefix). The invariant
// holds by arithmetic.
func TestValidateTenantNamespaceLength(t *testing.T) {
	tests := []struct {
		name             string
		currentNamespace string
		tenantName       string
		wantError        bool
	}{
		{
			name:             "tenant-root with short name passes",
			currentNamespace: "tenant-root",
			tenantName:       "alpha",
			wantError:        false,
		},
		{
			name:             "root tenant inside root namespace passes",
			currentNamespace: "tenant-root",
			tenantName:       "root",
			wantError:        false,
		},
		{
			name:             "short parent and short name passes",
			currentNamespace: "tenant-foo",
			tenantName:       "bar",
			wantError:        false,
		},
		{
			name:             "exactly at 63-char limit passes",
			currentNamespace: "tenant-" + strings.Repeat("a", 45), // 52 chars
			tenantName:       strings.Repeat("b", 10),             // 10 chars -> 52+1+10 = 63
			wantError:        false,
		},
		{
			name:             "one char over the limit fails",
			currentNamespace: "tenant-" + strings.Repeat("a", 45), // 52 chars
			tenantName:       strings.Repeat("b", 11),             // 11 chars -> 52+1+11 = 64
			wantError:        true,
		},
		{
			name:             "deeply nested failure with issue-style names",
			currentNamespace: "tenant-" + strings.Repeat("a", 35), // 42 chars
			tenantName:       strings.Repeat("b", 25),             // 25 chars -> 42+1+25 = 68
			wantError:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &REST{
				kindName: "Tenant",
				releaseConfig: config.ReleaseConfig{
					Prefix: "tenant-",
				},
			}

			errs := r.validateTenantNamespaceLength(tt.currentNamespace, tt.tenantName)

			if tt.wantError && len(errs) == 0 {
				computed := r.computeTenantNamespace(tt.currentNamespace, tt.tenantName)
				t.Errorf("expected error for parent=%q name=%q (computed=%q, len=%d), got none",
					tt.currentNamespace, tt.tenantName, computed, len(computed))
				return
			}
			if !tt.wantError && len(errs) > 0 {
				t.Errorf("unexpected error for parent=%q name=%q: %v",
					tt.currentNamespace, tt.tenantName, errs)
				return
			}

			// For failing cases, verify the error message surfaces the
			// computed namespace string and its actual length so a
			// regression in the message format is caught.
			if tt.wantError {
				computed := r.computeTenantNamespace(tt.currentNamespace, tt.tenantName)
				msg := errs.ToAggregate().Error()
				if !strings.Contains(msg, computed) {
					t.Errorf("error message must contain computed namespace %q, got: %s", computed, msg)
				}
				if !strings.Contains(msg, fmt.Sprintf("%d characters", len(computed))) {
					t.Errorf("error message must contain the computed length %d, got: %s", len(computed), msg)
				}
			}
		})
	}
}
