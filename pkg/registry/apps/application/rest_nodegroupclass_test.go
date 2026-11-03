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
	"strings"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"
)

func nodeGroupClassREST(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) *REST {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := helmv2.AddToScheme(scheme); err != nil {
		t.Fatalf("register helmv2 scheme: %v", err)
	}
	if err := cozyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("register cozystack scheme: %v", err)
	}
	resourceCfg := &config.ResourceConfig{
		Resources: []config.Resource{{Application: config.ApplicationConfig{Kind: kubernetesNodesKind}}},
	}
	if err := appsv1alpha1.RegisterDynamicTypes(scheme, resourceCfg); err != nil {
		t.Fatalf("register dynamic types: %v", err)
	}
	return &REST{
		c:        fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithInterceptorFuncs(funcs).Build(),
		gvr:      schema.GroupVersionResource{Group: appsv1alpha1.GroupName, Version: "v1alpha1", Resource: "kubernetesnodes"},
		gvk:      schema.GroupVersionKind{Group: appsv1alpha1.GroupName, Version: "v1alpha1", Kind: kubernetesNodesKind},
		kindName: kubernetesNodesKind,
		releaseConfig: config.ReleaseConfig{
			Prefix: "kubernetes-nodes-",
		},
	}
}

func nodeGroupClass(name string, deleting bool) *cozyv1alpha1.NodeGroupClass {
	ngc := &cozyv1alpha1.NodeGroupClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       cozyv1alpha1.NodeGroupClassSpec{Bootstrap: cozyv1alpha1.NodeGroupBootstrapKairos},
	}
	if deleting {
		now := metav1.Now()
		ngc.DeletionTimestamp = &now
		ngc.Finalizers = []string{"cozystack.io/nodegroupclass-in-use"}
	}
	return ngc
}

func poolApp(spec string) *appsv1alpha1.Application {
	return &appsv1alpha1.Application{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps.cozystack.io/v1alpha1", Kind: kubernetesNodesKind},
		ObjectMeta: metav1.ObjectMeta{Name: "k-md0", Namespace: "tenant-foo"},
		Spec:       &apiextensionsv1.JSON{Raw: []byte(spec)},
	}
}

func poolRelease(values string) *helmv2.HelmRelease {
	return &helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubernetes-nodes-k-md0",
			Namespace: "tenant-foo",
			Labels: map[string]string{
				ApplicationKindLabel:  kubernetesNodesKind,
				ApplicationGroupLabel: appsv1alpha1.GroupName,
				ApplicationNameLabel:  "k-md0",
			},
		},
		Spec: helmv2.HelmReleaseSpec{Values: &apiextensionsv1.JSON{Raw: []byte(values)}},
	}
}

var poolCtx = request.WithNamespace(context.Background(), "tenant-foo")

func wantRefusedForDeletion(t *testing.T, err error) {
	t.Helper()
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), `NodeGroupClass "acme-old" is being deleted`) {
		t.Fatalf("err = %v, want Invalid naming the class being deleted", err)
	}
}

func TestCreate_RefusesAPoolOnAClassBeingDeleted(t *testing.T) {
	r := nodeGroupClassREST(t, interceptor.Funcs{}, nodeGroupClass("acme-old", true))
	_, err := r.Create(poolCtx, poolApp(`{"cluster":"k","nodeGroupClassName":"acme-old"}`), nil, &metav1.CreateOptions{})
	wantRefusedForDeletion(t, err)
}

func TestCreate_AcceptsAPoolOnALiveClass(t *testing.T) {
	r := nodeGroupClassREST(t, interceptor.Funcs{}, nodeGroupClass("acme-new", false))
	if _, err := r.Create(poolCtx, poolApp(`{"cluster":"k","nodeGroupClassName":"acme-new"}`), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

// A missing class is the chart's to report: it fails the render naming it.
func TestCreate_AcceptsAPoolOnAMissingClass(t *testing.T) {
	r := nodeGroupClassREST(t, interceptor.Funcs{})
	if _, err := r.Create(poolCtx, poolApp(`{"cluster":"k","nodeGroupClassName":"acme-missing"}`), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func TestUpdate_RefusesMovingAPoolOntoAClassBeingDeleted(t *testing.T) {
	r := nodeGroupClassREST(t, interceptor.Funcs{},
		nodeGroupClass("acme-old", true),
		nodeGroupClass("acme-new", false),
		poolRelease(`{"cluster":"k","nodeGroupClassName":"acme-new"}`),
	)
	_, _, err := r.Update(poolCtx, "k-md0",
		newDefaultUpdatedObjectInfo(poolApp(`{"cluster":"k","nodeGroupClassName":"acme-old"}`)),
		nil, nil, false, &metav1.UpdateOptions{})
	wantRefusedForDeletion(t, err)
}

// A pool already on the class keeps rendering and can still be edited.
func TestUpdate_AcceptsAPoolThatStaysOnAClassBeingDeleted(t *testing.T) {
	r := nodeGroupClassREST(t, interceptor.Funcs{},
		nodeGroupClass("acme-old", true),
		poolRelease(`{"cluster":"k","nodeGroupClassName":"acme-old","maxReplicas":1}`),
	)
	_, _, err := r.Update(poolCtx, "k-md0",
		newDefaultUpdatedObjectInfo(poolApp(`{"cluster":"k","nodeGroupClassName":"acme-old","maxReplicas":2}`)),
		nil, nil, false, &metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// classUnreadable fails every NodeGroupClass read and passes the rest through.
var classUnreadable = interceptor.Funcs{
	Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*cozyv1alpha1.NodeGroupClass); ok {
			return apierrors.NewForbidden(schema.GroupResource{Group: "cozystack.io", Resource: "nodegroupclasses"}, key.Name, nil)
		}
		return c.Get(ctx, key, obj, opts...)
	},
}

func wantClassReadFailure(t *testing.T, err error, name string) {
	t.Helper()
	if !apierrors.IsInternalError(err) || !strings.Contains(err.Error(), `reading NodeGroupClass "`+name+`"`) {
		t.Fatalf("err = %v, want an internal error carrying the read failure", err)
	}
}

// A pool cannot be checked against a class the API cannot read, so it is
// refused rather than let through.
func TestCreate_RefusesAPoolWhenTheClassCannotBeRead(t *testing.T) {
	r := nodeGroupClassREST(t, classUnreadable)
	_, err := r.Create(poolCtx, poolApp(`{"cluster":"k","nodeGroupClassName":"acme-new"}`), nil, &metav1.CreateOptions{})
	wantClassReadFailure(t, err, "acme-new")
}

func TestUpdate_RefusesMovingAPoolWhenTheClassCannotBeRead(t *testing.T) {
	r := nodeGroupClassREST(t, classUnreadable, poolRelease(`{"cluster":"k","nodeGroupClassName":"acme-new"}`))
	_, _, err := r.Update(poolCtx, "k-md0",
		newDefaultUpdatedObjectInfo(poolApp(`{"cluster":"k","nodeGroupClassName":"acme-old"}`)),
		nil, nil, false, &metav1.UpdateOptions{})
	wantClassReadFailure(t, err, "acme-old")
}

// Pools resolve the built-in classes without the object, so a talos object
// in deletion does not stand in their way.
func TestCreate_AcceptsAPoolOnABuiltInClassWhoseObjectIsBeingDeleted(t *testing.T) {
	r := nodeGroupClassREST(t, interceptor.Funcs{}, nodeGroupClass("talos", true))
	if _, err := r.Create(poolCtx, poolApp(`{"cluster":"k","nodeGroupClassName":"talos"}`), nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func TestCreate_IgnoresTheFieldOnOtherKinds(t *testing.T) {
	r := nodeGroupClassREST(t, interceptor.Funcs{}, nodeGroupClass("acme-old", true))
	r.kindName = "Kubernetes"
	if errs, err := r.validateNodeGroupClass(poolCtx, poolApp(`{"nodeGroupClassName":"acme-old"}`), nil); len(errs) != 0 || err != nil {
		t.Fatalf("validateNodeGroupClass on a Kubernetes application = %v, %v, want none", errs, err)
	}
}
