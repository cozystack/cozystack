package lineagelabeler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/shared/crdmem"
	"github.com/cozystack/cozystack/pkg/lineage"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var ErrNoAncestors = errors.New("no ancestors")

type LineageLabelerReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	WatchResourceCSV string

	dynClient dynamic.Interface
	mapper    meta.RESTMapper

	appMap atomic.Value
	once   sync.Once

	mem *crdmem.Memory
}

type chartRef struct{ repo, chart string }
type appRef struct{ groupVersion, kind, prefix string }

func (r *LineageLabelerReconciler) initMapping() {
	r.once.Do(func() {
		r.appMap.Store(make(map[chartRef]appRef))
	})
}

func (r *LineageLabelerReconciler) currentMap() map[chartRef]appRef {
	val := r.appMap.Load()
	if val == nil {
		return map[chartRef]appRef{}
	}
	return val.(map[chartRef]appRef)
}

func (r *LineageLabelerReconciler) Map(hr *helmv2.HelmRelease) (string, string, string, error) {
	cfg := r.currentMap()
	s := hr.Spec.Chart.Spec
	key := chartRef{s.SourceRef.Name, s.Chart}
	if v, ok := cfg[key]; ok {
		return v.groupVersion, v.kind, v.prefix, nil
	}
	return "", "", "", fmt.Errorf("cannot map helm release %s/%s to dynamic app", hr.Namespace, hr.Name)
}

func parseGVKList(csv string) ([]schema.GroupVersionKind, error) {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil, fmt.Errorf("watch resource list is empty")
	}
	parts := strings.Split(csv, ",")
	out := make([]schema.GroupVersionKind, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		s := strings.Split(p, "/")
		if len(s) == 2 {
			out = append(out, schema.GroupVersionKind{Group: "", Version: s[0], Kind: s[1]})
			continue
		}
		if len(s) == 3 {
			out = append(out, schema.GroupVersionKind{Group: s[0], Version: s[1], Kind: s[2]})
			continue
		}
		return nil, fmt.Errorf("invalid resource token %q, expected 'group/version/Kind' or 'v1/Kind'", p)
	}
	return out, nil
}

func (r *LineageLabelerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.initMapping()

	cfg := rest.CopyConfig(mgr.GetConfig())
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	disco, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return err
	}
	cached := memory.NewMemCacheClient(disco)
	r.dynClient = dc
	r.mapper = restmapper.NewDeferredDiscoveryRESTMapper(cached)

	if r.mem == nil {
		r.mem = crdmem.Global()
	}
	if err := r.mem.EnsurePrimingWithManager(mgr); err != nil {
		return err
	}

	gvks, err := parseGVKList(r.WatchResourceCSV)
	if err != nil {
		return err
	}
	if len(gvks) == 0 {
		return fmt.Errorf("no resources to watch")
	}

	b := ctrl.NewControllerManagedBy(mgr).Named("lineage-labeler")

	nsPred := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		ns := obj.GetNamespace()
		return ns != "" && strings.HasPrefix(ns, "tenant-")
	})

	primary := gvks[0]
	primaryObj := &unstructured.Unstructured{}
	primaryObj.SetGroupVersionKind(primary)
	b = b.For(primaryObj,
		builder.WithPredicates(
			predicate.And(
				nsPred,
				predicate.Or(
					predicate.GenerationChangedPredicate{},
					predicate.ResourceVersionChangedPredicate{},
				),
			),
		),
	)

	for _, gvk := range gvks[1:] {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		b = b.Watches(u,
			&handler.EnqueueRequestForObject{},
			builder.WithPredicates(
				predicate.And(
					nsPred,
					predicate.Or(
						predicate.GenerationChangedPredicate{},
						predicate.ResourceVersionChangedPredicate{},
					),
				),
			),
		)
	}

	b = b.Watches(
		&cozyv1alpha1.CozystackResourceDefinition{},
		handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			_ = r.refreshAppMap(ctx)
			return nil
		}),
	)

	_ = r.refreshAppMap(context.Background())

	return b.Complete(r)
}

func (r *LineageLabelerReconciler) refreshAppMap(ctx context.Context) error {
	var items []cozyv1alpha1.CozystackResourceDefinition
	var err error
	if r.mem != nil {
		items, err = r.mem.ListFromCacheOrAPI(ctx, r.Client)
	} else {
		var list cozyv1alpha1.CozystackResourceDefinitionList
		err = r.Client.List(ctx, &list)
		items = list.Items
	}
	if err != nil {
		return err
	}
	newMap := make(map[chartRef]appRef, len(items))
	for _, crd := range items {
		k := chartRef{
			repo:  crd.Spec.Release.Chart.SourceRef.Name,
			chart: crd.Spec.Release.Chart.Name,
		}
		v := appRef{
			groupVersion: "apps.cozystack.io/v1alpha1",
			kind:         crd.Spec.Application.Kind,
			prefix:       crd.Spec.Release.Prefix,
		}
		if _, exists := newMap[k]; exists {
			continue
		}
		newMap[k] = v
	}
	r.appMap.Store(newMap)
	return nil
}

func (r *LineageLabelerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	if req.Namespace == "" || !strings.HasPrefix(req.Namespace, "tenant-") {
		return ctrl.Result{}, nil
	}

	if len(r.currentMap()) == 0 {
		_ = r.refreshAppMap(ctx)
		if len(r.currentMap()) == 0 {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
	}

	gvks, err := parseGVKList(r.WatchResourceCSV)
	if err != nil {
		return ctrl.Result{}, err
	}

	var obj *unstructured.Unstructured
	found := false

	for _, gvk := range gvks {
		mapping, mErr := r.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if mErr != nil {
			continue
		}
		ns := req.Namespace
		if mapping.Scope.Name() != meta.RESTScopeNameNamespace {
			ns = ""
		}
		res, gErr := r.dynClient.Resource(mapping.Resource).Namespace(ns).Get(ctx, req.Name, metav1.GetOptions{})
		if gErr != nil {
			if apierrors.IsNotFound(gErr) {
				continue
			}
			continue
		}
		obj = res
		found = true
		break
	}

	if !found || obj == nil {
		return ctrl.Result{}, nil
	}

	existing := obj.GetLabels()
	if existing == nil {
		existing = map[string]string{}
	}

	keys := []string{
		"apps.cozystack.io/application.group",
		"apps.cozystack.io/application.kind",
		"apps.cozystack.io/application.name",
	}
	allPresent := true
	for _, k := range keys {
		if _, ok := existing[k]; !ok {
			allPresent = false
			break
		}
	}
	if allPresent {
		return ctrl.Result{}, nil
	}

	labels, warn, err := r.computeLabels(ctx, obj)
	if err != nil {
		if errors.Is(err, ErrNoAncestors) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if warn != "" {
		l.V(1).Info("lineage ambiguous; using first ancestor", "name", req.NamespacedName)
	}

	for k, v := range labels {
		existing[k] = v
	}
	obj.SetLabels(existing)

	// Server-Side Apply: claim ownership of our label keys
	gvk := obj.GroupVersionKind()
	patch := &unstructured.Unstructured{}
	patch.SetGroupVersionKind(gvk)
	patch.SetNamespace(obj.GetNamespace())
	patch.SetName(obj.GetName())
	patch.SetLabels(map[string]string{
		"apps.cozystack.io/application.group": existing["apps.cozystack.io/application.group"],
		"apps.cozystack.io/application.kind":  existing["apps.cozystack.io/application.kind"],
		"apps.cozystack.io/application.name":  existing["apps.cozystack.io/application.name"],
	})

	// Use controller-runtime client with Apply patch type and field owner
	if err := r.Patch(ctx, patch,
		client.Apply,
		client.FieldOwner("cozystack/lineage"),
		client.ForceOwnership(false),
	); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{RequeueAfter: 500 * time.Millisecond}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *LineageLabelerReconciler) computeLabels(ctx context.Context, o *unstructured.Unstructured) (map[string]string, string, error) {
	owners := lineage.WalkOwnershipGraph(ctx, r.dynClient, r.mapper, r, o)
	if len(owners) == 0 {
		return nil, "", ErrNoAncestors
	}
	obj, err := owners[0].GetUnstructured(ctx, r.dynClient, r.mapper)
	if err != nil {
		return nil, "", err
	}
	gv, err := schema.ParseGroupVersion(obj.GetAPIVersion())
	if err != nil {
		return nil, "", fmt.Errorf("invalid APIVersion %s: %w", obj.GetAPIVersion(), err)
	}
	var warn string
	if len(owners) > 1 {
		warn = "ambiguous"
	}
	group := gv.Group
	if len(group) > 63 {
		group = trimDNSLabel(group[:63])
	}
	return map[string]string{
		"apps.cozystack.io/application.group": group,
		"apps.cozystack.io/application.kind":  obj.GetKind(),
		"apps.cozystack.io/application.name":  obj.GetName(),
	}, warn, nil
}

func trimDNSLabel(s string) string {
	for len(s) > 0 {
		b := s[len(s)-1]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') {
			return s
		}
		s = s[:len(s)-1]
	}
	return s
}
