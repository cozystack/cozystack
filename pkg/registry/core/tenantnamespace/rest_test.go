// SPDX-License-Identifier: Apache-2.0

package tenantnamespace

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
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

// TestList_FiltersUnauthorizedNamespaces asserts the List path returns only
// tenant namespaces the user can access: RoleBindings for other users don't
// grant anything, and multiple matching RoleBindings in one namespace don't
// duplicate it in the result.
func TestList_FiltersUnauthorizedNamespaces(t *testing.T) {
	roleRef := rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "test-role"}
	client := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-allowed"}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-denied"}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
			&rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "user-binding", Namespace: "tenant-allowed"},
				Subjects:   []rbacv1.Subject{{Kind: "User", Name: "test-user", APIGroup: "rbac.authorization.k8s.io"}},
				RoleRef:    roleRef,
			},
			&rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "group-binding", Namespace: "tenant-allowed"},
				Subjects:   []rbacv1.Subject{{Kind: "Group", Name: "system:authenticated", APIGroup: "rbac.authorization.k8s.io"}},
				RoleRef:    roleRef,
			},
			&rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "other-binding", Namespace: "tenant-denied"},
				Subjects:   []rbacv1.Subject{{Kind: "User", Name: "other-user", APIGroup: "rbac.authorization.k8s.io"}},
				RoleRef:    roleRef,
			},
		).
		Build()

	r := &REST{
		c:   client,
		gvr: schema.GroupVersionResource{Group: "core.cozystack.io", Version: "v1alpha1", Resource: "tenantnamespaces"},
	}

	u := &user.DefaultInfo{Name: "test-user", Groups: []string{"system:authenticated"}}
	ctx := request.WithUser(context.Background(), u)

	obj, err := r.List(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	list, ok := obj.(*corev1alpha1.TenantNamespaceList)
	if !ok {
		t.Fatalf("expected *TenantNamespaceList, got %T", obj)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 item, got %d: %+v", len(list.Items), list.Items)
	}
	if list.Items[0].Name != "tenant-allowed" {
		t.Errorf("expected tenant-allowed, got %q", list.Items[0].Name)
	}
}

// TestList_PrivilegedGroups_SeeAllTenantNamespaces asserts privileged groups
// get every tenant namespace (and only tenant namespaces) without any
// RoleBindings, sorted by name.
func TestList_PrivilegedGroups_SeeAllTenantNamespaces(t *testing.T) {
	for _, group := range []string{"system:masters", "cozystack-cluster-admin"} {
		t.Run(group, func(t *testing.T) {
			client := fake.NewClientBuilder().
				WithScheme(newTestScheme(t)).
				WithObjects(
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-zebra"}},
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-alpha"}},
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
				).
				Build()

			r := &REST{
				c:   client,
				gvr: schema.GroupVersionResource{Group: "core.cozystack.io", Version: "v1alpha1", Resource: "tenantnamespaces"},
			}

			u := &user.DefaultInfo{Name: "admin", Groups: []string{group}}
			ctx := request.WithUser(context.Background(), u)

			obj, err := r.List(ctx, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			list, ok := obj.(*corev1alpha1.TenantNamespaceList)
			if !ok {
				t.Fatalf("expected *TenantNamespaceList, got %T", obj)
			}
			if len(list.Items) != 2 {
				t.Fatalf("expected 2 items, got %d: %+v", len(list.Items), list.Items)
			}
			for i, want := range []string{"tenant-alpha", "tenant-zebra"} {
				if list.Items[i].Name != want {
					t.Errorf("item %d: expected %q, got %q", i, want, list.Items[i].Name)
				}
			}
		})
	}
}

// TestList_MissingUser_ReturnsError asserts List fails when no user is in the
// context instead of returning an unfiltered list.
func TestList_MissingUser_ReturnsError(t *testing.T) {
	client := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"}}).
		Build()

	r := &REST{
		c:   client,
		gvr: schema.GroupVersionResource{Group: "core.cozystack.io", Version: "v1alpha1", Resource: "tenantnamespaces"},
	}

	obj, err := r.List(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for missing user in context, got nil")
	}
	if obj != nil {
		t.Errorf("expected nil list on error, got %+v", obj)
	}
}

// TestList_RoleBindingInNonTenantNamespace_Ignored asserts a RoleBinding
// outside the tenant namespace set grants nothing: filterAccessible
// intersects RoleBinding namespaces with the tenant name-set.
func TestList_RoleBindingInNonTenantNamespace_Ignored(t *testing.T) {
	client := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
			&rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "default-binding", Namespace: "default"},
				Subjects:   []rbacv1.Subject{{Kind: "User", Name: "test-user", APIGroup: "rbac.authorization.k8s.io"}},
				RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "test-role"},
			},
		).
		Build()

	r := &REST{
		c:   client,
		gvr: schema.GroupVersionResource{Group: "core.cozystack.io", Version: "v1alpha1", Resource: "tenantnamespaces"},
	}

	u := &user.DefaultInfo{Name: "test-user", Groups: []string{"system:authenticated"}}
	ctx := request.WithUser(context.Background(), u)

	obj, err := r.List(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	list, ok := obj.(*corev1alpha1.TenantNamespaceList)
	if !ok {
		t.Fatalf("expected *TenantNamespaceList, got %T", obj)
	}
	if len(list.Items) != 0 {
		t.Errorf("expected empty list, got %+v", list.Items)
	}
}

// TestList_NamespaceListError_Propagates asserts a failure to list namespaces
// is returned to the caller.
func TestList_NamespaceListError_Propagates(t *testing.T) {
	wantErr := errors.New("namespace list failed")
	fc := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*corev1.NamespaceList); ok {
					return wantErr
				}
				return c.List(ctx, list, opts...)
			},
		}).
		Build()

	r := &REST{
		c:   fc,
		gvr: schema.GroupVersionResource{Group: "core.cozystack.io", Version: "v1alpha1", Resource: "tenantnamespaces"},
	}

	u := &user.DefaultInfo{Name: "test-user", Groups: []string{"system:authenticated"}}
	ctx := request.WithUser(context.Background(), u)

	if _, err := r.List(ctx, nil); !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
}

// TestList_RoleBindingListError_Propagates asserts a failure to list
// RoleBindings during access filtering fails the List instead of returning an
// unfiltered or silently truncated result.
func TestList_RoleBindingListError_Propagates(t *testing.T) {
	wantErr := errors.New("rolebinding list failed")
	fc := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"}}).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*rbacv1.RoleBindingList); ok {
					return wantErr
				}
				return c.List(ctx, list, opts...)
			},
		}).
		Build()

	r := &REST{
		c:   fc,
		gvr: schema.GroupVersionResource{Group: "core.cozystack.io", Version: "v1alpha1", Resource: "tenantnamespaces"},
	}

	u := &user.DefaultInfo{Name: "test-user", Groups: []string{"system:authenticated"}}
	ctx := request.WithUser(context.Background(), u)

	if _, err := r.List(ctx, nil); !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
}

// TestGet_MissingUser_ReturnsError asserts Get fails when no user is in the
// context.
func TestGet_MissingUser_ReturnsError(t *testing.T) {
	client := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-test"}}).
		Build()

	r := &REST{
		c:   client,
		gvr: schema.GroupVersionResource{Group: "core.cozystack.io", Version: "v1alpha1", Resource: "tenantnamespaces"},
	}

	if _, err := r.Get(context.Background(), "tenant-test", &metav1.GetOptions{}); err == nil {
		t.Fatal("expected error for missing user in context, got nil")
	}
}

// TestGet_NamespaceNotFound asserts the backing Get error surfaces when the
// namespace disappears after the access check passes.
func TestGet_NamespaceNotFound(t *testing.T) {
	client := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()

	r := &REST{
		c:   client,
		gvr: schema.GroupVersionResource{Group: "core.cozystack.io", Version: "v1alpha1", Resource: "tenantnamespaces"},
	}

	u := &user.DefaultInfo{Name: "admin", Groups: []string{"system:masters"}}
	ctx := request.WithUser(context.Background(), u)

	_, err := r.Get(ctx, "tenant-missing", &metav1.GetOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

// TestMatchesSubject_UnknownKind asserts unknown subject kinds never match.
func TestMatchesSubject_UnknownKind(t *testing.T) {
	subj := rbacv1.Subject{Kind: "UnknownKind", Name: "test-user"}
	if matchesSubject(subj, "tenant-test", "test-user", map[string]struct{}{}) {
		t.Error("expected unknown subject kind not to match")
	}
}

func TestHasAccessToNamespace_MissingUser(t *testing.T) {
	client := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		Build()

	r := &REST{
		c:   client,
		gvr: schema.GroupVersionResource{Group: "core.cozystack.io", Version: "v1alpha1", Resource: "tenantnamespaces"},
	}

	hasAccess, err := r.hasAccessToNamespace(context.Background(), "tenant-test")
	if err == nil {
		t.Fatal("expected error for missing user in context, got nil")
	}
	if hasAccess {
		t.Error("expected no access when user is missing in context")
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
	tn, ok := obj.(*corev1alpha1.TenantNamespace)
	if !ok {
		t.Fatalf("expected *TenantNamespace, got %T", obj)
	}
	if tn.Name != "tenant-test" {
		t.Errorf("expected name %q, got %q", "tenant-test", tn.Name)
	}
	if tn.Kind != "TenantNamespace" {
		t.Errorf("expected Kind=TenantNamespace, got %q", tn.Kind)
	}
	if tn.APIVersion != corev1alpha1.SchemeGroupVersion.String() {
		t.Errorf("expected APIVersion=%q, got %q", corev1alpha1.SchemeGroupVersion.String(), tn.APIVersion)
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
