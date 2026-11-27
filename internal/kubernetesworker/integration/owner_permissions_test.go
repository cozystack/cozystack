//go:build integration

package integration

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	authz "k8s.io/api/authorization/v1"
	core "k8s.io/api/core/v1"
	ext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestKairosTemplateOwnerPermissions(t *testing.T) {
	if os.Getenv("RUN_WORKERCLASS_INTEGRATION") != "1" {
		t.Skip("set RUN_WORKERCLASS_INTEGRATION=1 with local envtest assets")
	}
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		t.Fatal("KUBEBUILDER_ASSETS must name local control-plane binaries")
	}
	childCRD := crd("bootstrap.cluster.x-k8s.io", "kairosconfigtemplates", "KairosConfigTemplate", "Namespaced")
	childCRD.Spec.Versions[0].Name = "v1beta2"
	env := &envtest.Environment{BinaryAssetsDirectory: assets, ControlPlaneStartTimeout: 45 * time.Second, ControlPlaneStopTimeout: 15 * time.Second, CRDs: []*ext.CustomResourceDefinition{crd("controlplane.cluster.x-k8s.io", "kamajicontrolplanes", "KamajiControlPlane", "Namespaced"), childCRD}}
	env.ControlPlane.GetAPIServer().Configure().Set("enable-admission-plugins", "OwnerReferencesPermissionEnforcement")
	cfg, err := env.Start()
	check(t, err)
	t.Cleanup(func() { check(t, env.Stop()) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := dynamic.NewForConfig(cfg)
	check(t, err)
	kube, err := kubernetes.NewForConfig(cfg)
	check(t, err)
	_, err = kube.CoreV1().Namespaces().Create(ctx, &core.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
	check(t, err)
	rendered, err := exec.CommandContext(ctx, "helm", "template", "kubernetes-nodes-k-md0", "../../../packages/apps/kubernetes-nodes", "--namespace", ns, "--set", "cluster=k,workerClassName=kairos,_cluster.cluster-domain=cozy.local,instanceType=,resources.cpu=2,resources.memory=4Gi", "--show-only", "templates/kairos-reconcile-job.yaml").Output()
	if err != nil {
		t.Fatalf("render: %v\n%s", err, rendered)
	}
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(rendered), 4096)
	var sa, template string
	for {
		obj := &unstructured.Unstructured{}
		err := decoder.Decode(obj)
		if err == io.EOF {
			break
		}
		check(t, err)
		var gvr schema.GroupVersionResource
		switch obj.GetKind() {
		case "ConfigMap":
			template = obj.GetName()
			continue
		case "ServiceAccount":
			sa = obj.GetName()
			gvr = schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}
		case "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding":
			gvr = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: strings.ToLower(obj.GetKind()) + "s"}
		default:
			continue
		}
		_, err = admin.Resource(gvr).Namespace(obj.GetNamespace()).Create(ctx, obj, metav1.CreateOptions{})
		check(t, err)
	}
	if sa == "" || template == "" {
		t.Fatal("render omitted reconcile identity or template")
	}
	ownerGVR := schema.GroupVersionResource{Group: "controlplane.cluster.x-k8s.io", Version: "v1alpha1", Resource: "kamajicontrolplanes"}
	owner := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "controlplane.cluster.x-k8s.io/v1alpha1", "kind": "KamajiControlPlane", "metadata": map[string]any{"name": "kubernetes-k", "namespace": ns}}}
	owner, err = admin.Resource(ownerGVR).Namespace(ns).Create(ctx, owner, metav1.CreateOptions{})
	check(t, err)
	userCfg := rest.CopyConfig(cfg)
	userCfg.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:" + ns + ":" + sa, Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + ns, "system:authenticated"}}
	user, err := dynamic.NewForConfig(userCfg)
	check(t, err)
	userKube, err := kubernetes.NewForConfig(userCfg)
	check(t, err)
	childGVR := schema.GroupVersionResource{Group: "bootstrap.cluster.x-k8s.io", Version: "v1beta2", Resource: "kairosconfigtemplates"}
	child := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "bootstrap.cluster.x-k8s.io/v1beta2", "kind": "KairosConfigTemplate", "metadata": map[string]any{"name": template, "namespace": ns}}}
	child.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: owner.GetAPIVersion(), Kind: owner.GetKind(), Name: owner.GetName(), UID: owner.GetUID(), Controller: new(true), BlockOwnerDeletion: new(false)}})
	_, err = user.Resource(childGVR).Namespace(ns).Create(ctx, child, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	check(t, err)
	blocking := child.GetOwnerReferences()
	blocking[0].BlockOwnerDeletion = new(true)
	child.SetOwnerReferences(blocking)
	_, err = user.Resource(childGVR).Namespace(ns).Create(ctx, child, metav1.CreateOptions{})
	check(t, err)
	for _, attrs := range []authz.ResourceAttributes{
		{Namespace: ns, Verb: "update", Group: ownerGVR.Group, Resource: ownerGVR.Resource, Subresource: "finalizers", Name: "another-control-plane"},
		{Namespace: ns, Verb: "update", Group: ownerGVR.Group, Resource: ownerGVR.Resource, Name: owner.GetName()},
	} {
		review, err := userKube.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authz.SelfSubjectAccessReview{Spec: authz.SelfSubjectAccessReviewSpec{ResourceAttributes: &attrs}}, metav1.CreateOptions{})
		check(t, err)
		if review.Status.Allowed {
			t.Fatalf("unrelated owner permission allowed: %+v", attrs)
		}
	}
	child.SetName(template + "-another-owner")
	refs := child.GetOwnerReferences()
	refs[0].Name = "another-control-plane"
	child.SetOwnerReferences(refs)
	_, err = user.Resource(childGVR).Namespace(ns).Create(ctx, child, metav1.CreateOptions{})
	if !apierrors.IsForbidden(err) {
		t.Fatalf("blocking reference to unrelated owner: %v", err)
	}
}
