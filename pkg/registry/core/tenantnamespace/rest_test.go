// SPDX-License-Identifier: Apache-2.0

package tenantnamespace

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMakeListSortsAlphabetically(t *testing.T) {
	r := &REST{}

	// Create namespaces in non-alphabetical order
	src := &corev1.NamespaceList{
		Items: []corev1.Namespace{
			{ObjectMeta: metav1.ObjectMeta{Name: "tenant-zebra"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "tenant-alpha"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "tenant-mike"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "tenant-bravo"}},
		},
	}

	allowed := []string{"tenant-zebra", "tenant-alpha", "tenant-mike", "tenant-bravo"}

	result := r.makeList(src, allowed)

	expected := []string{"tenant-alpha", "tenant-bravo", "tenant-mike", "tenant-zebra"}

	if len(result.Items) != len(expected) {
		t.Fatalf("expected %d items, got %d", len(expected), len(result.Items))
	}

	for i, name := range expected {
		if result.Items[i].Name != name {
			t.Errorf("item %d: expected %q, got %q", i, name, result.Items[i].Name)
		}
	}
}

// Security tests for IDOR fix

func TestHasAccessToNamespace_WithUserAccess(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"},
	}

	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-binding",
			Namespace: "tenant-test",
		},
		Subjects: []rbacv1.Subject{
			{Kind: "User", Name: "test-user", APIGroup: "rbac.authorization.k8s.io"},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     "test-role",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ns, rb).
		Build()

	r := &REST{
		c:   client,
		gvr: schema.GroupVersionResource{Group: "core.cozystack.io", Version: "v1alpha1", Resource: "tenantnamespaces"},
	}

	u := &user.DefaultInfo{
		Name:   "test-user",
		Groups: []string{"system:authenticated"},
	}
	ctx := request.WithUser(context.Background(), u)

	hasAccess, err := r.hasAccessToNamespace(ctx, "tenant-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasAccess {
		t.Error("expected user to have access, but got false")
	}
}

func TestHasAccessToNamespace_WithoutAccess(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"},
	}

	// RoleBinding for different user
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-binding",
			Namespace: "tenant-test",
		},
		Subjects: []rbacv1.Subject{
			{Kind: "User", Name: "other-user", APIGroup: "rbac.authorization.k8s.io"},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     "test-role",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ns, rb).
		Build()

	r := &REST{
		c:   client,
		gvr: schema.GroupVersionResource{Group: "core.cozystack.io", Version: "v1alpha1", Resource: "tenantnamespaces"},
	}

	u := &user.DefaultInfo{
		Name:   "test-user",
		Groups: []string{"system:authenticated"},
	}
	ctx := request.WithUser(context.Background(), u)

	hasAccess, err := r.hasAccessToNamespace(ctx, "tenant-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasAccess {
		t.Error("expected user to NOT have access, but got true")
	}
}

func TestHasAccessToNamespace_WithGroupAccess(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"},
	}

	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-binding",
			Namespace: "tenant-test",
		},
		Subjects: []rbacv1.Subject{
			{Kind: "Group", Name: "test-group", APIGroup: "rbac.authorization.k8s.io"},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     "test-role",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ns, rb).
		Build()

	r := &REST{
		c:   client,
		gvr: schema.GroupVersionResource{Group: "core.cozystack.io", Version: "v1alpha1", Resource: "tenantnamespaces"},
	}

	u := &user.DefaultInfo{
		Name:   "test-user",
		Groups: []string{"system:authenticated", "test-group"},
	}
	ctx := request.WithUser(context.Background(), u)

	hasAccess, err := r.hasAccessToNamespace(ctx, "tenant-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasAccess {
		t.Error("expected user to have access via group, but got false")
	}
}

func TestHasAccessToNamespace_SystemMasters(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ns).
		Build()

	r := &REST{
		c:   client,
		gvr: schema.GroupVersionResource{Group: "core.cozystack.io", Version: "v1alpha1", Resource: "tenantnamespaces"},
	}

	u := &user.DefaultInfo{
		Name:   "admin",
		Groups: []string{"system:masters"},
	}
	ctx := request.WithUser(context.Background(), u)

	hasAccess, err := r.hasAccessToNamespace(ctx, "tenant-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasAccess {
		t.Error("expected system:masters to have access, but got false")
	}
}

func TestHasAccessToNamespace_CozyAdminGroup(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ns).
		Build()

	r := &REST{
		c:   client,
		gvr: schema.GroupVersionResource{Group: "core.cozystack.io", Version: "v1alpha1", Resource: "tenantnamespaces"},
	}

	u := &user.DefaultInfo{
		Name:   "cozy-admin",
		Groups: []string{"cozystack-cluster-admin"},
	}
	ctx := request.WithUser(context.Background(), u)

	hasAccess, err := r.hasAccessToNamespace(ctx, "tenant-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasAccess {
		t.Error("expected cozystack-cluster-admin to have access, but got false")
	}
}

func TestHasAccessToNamespace_ServiceAccount(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"},
	}

	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-binding",
			Namespace: "tenant-test",
		},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: "test-sa", Namespace: "tenant-test"},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     "test-role",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ns, rb).
		Build()

	r := &REST{
		c:   client,
		gvr: schema.GroupVersionResource{Group: "core.cozystack.io", Version: "v1alpha1", Resource: "tenantnamespaces"},
	}

	u := &user.DefaultInfo{
		Name:   "system:serviceaccount:tenant-test:test-sa",
		Groups: []string{"system:authenticated"},
	}
	ctx := request.WithUser(context.Background(), u)

	hasAccess, err := r.hasAccessToNamespace(ctx, "tenant-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasAccess {
		t.Error("expected service account to have access, but got false")
	}
}

func TestHasAccessToNamespace_ServiceAccountEmptyNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"},
	}

	// ServiceAccount subject with empty namespace should default to RoleBinding namespace
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-binding",
			Namespace: "tenant-test",
		},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: "test-sa", Namespace: ""}, // Empty namespace
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     "test-role",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ns, rb).
		Build()

	r := &REST{
		c:   client,
		gvr: schema.GroupVersionResource{Group: "core.cozystack.io", Version: "v1alpha1", Resource: "tenantnamespaces"},
	}

	u := &user.DefaultInfo{
		Name:   "system:serviceaccount:tenant-test:test-sa",
		Groups: []string{"system:authenticated"},
	}
	ctx := request.WithUser(context.Background(), u)

	hasAccess, err := r.hasAccessToNamespace(ctx, "tenant-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasAccess {
		t.Error("expected service account with empty namespace to have access, but got false")
	}
}

func TestGet_WithAccess(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"},
	}

	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-binding",
			Namespace: "tenant-test",
		},
		Subjects: []rbacv1.Subject{
			{Kind: "User", Name: "test-user", APIGroup: "rbac.authorization.k8s.io"},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     "test-role",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ns, rb).
		Build()

	r := &REST{
		c:   client,
		gvr: schema.GroupVersionResource{Group: "core.cozystack.io", Version: "v1alpha1", Resource: "tenantnamespaces"},
	}

	u := &user.DefaultInfo{
		Name:   "test-user",
		Groups: []string{"system:authenticated"},
	}
	ctx := request.WithUser(context.Background(), u)

	obj, err := r.Get(ctx, "tenant-test", &metav1.GetOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obj == nil {
		t.Fatal("expected object, got nil")
	}
}

func TestGet_WithoutAccess(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"},
	}

	// RoleBinding for different user
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-binding",
			Namespace: "tenant-test",
		},
		Subjects: []rbacv1.Subject{
			{Kind: "User", Name: "other-user", APIGroup: "rbac.authorization.k8s.io"},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     "test-role",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ns, rb).
		Build()

	r := &REST{
		c:   client,
		gvr: schema.GroupVersionResource{Group: "core.cozystack.io", Version: "v1alpha1", Resource: "tenantnamespaces"},
	}

	u := &user.DefaultInfo{
		Name:   "test-user",
		Groups: []string{"system:authenticated"},
	}
	ctx := request.WithUser(context.Background(), u)

	obj, err := r.Get(ctx, "tenant-test", &metav1.GetOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if obj != nil {
		t.Errorf("expected nil object, got %v", obj)
	}

	// Verify it returns Forbidden to follow standard K8s RBAC behavior
	if !apierrors.IsForbidden(err) {
		t.Errorf("expected Forbidden error, got %v", err)
	}
}

func TestGet_NonTenantNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	r := &REST{
		c:   client,
		gvr: schema.GroupVersionResource{Group: "core.cozystack.io", Version: "v1alpha1", Resource: "tenantnamespaces"},
	}

	u := &user.DefaultInfo{
		Name:   "test-user",
		Groups: []string{"system:masters"},
	}
	ctx := request.WithUser(context.Background(), u)

	obj, err := r.Get(ctx, "default", &metav1.GetOptions{})
	if err == nil {
		t.Fatal("expected error for non-tenant namespace, got nil")
	}
	if obj != nil {
		t.Errorf("expected nil object, got %v", obj)
	}
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected NotFound error, got %v", err)
	}
}
