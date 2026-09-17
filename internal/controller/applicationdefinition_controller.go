package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/pkg/config"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type ApplicationDefinitionReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Debounce time.Duration

	mu          sync.Mutex
	lastEvent   time.Time
	lastHandled time.Time
}

func (r *ApplicationDefinitionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Only handle debounced restart logic
	// HelmRelease reconciliation is handled by ApplicationDefinitionHelmReconciler
	return r.debouncedRestart(ctx)
}

func (r *ApplicationDefinitionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Debounce == 0 {
		r.Debounce = 5 * time.Second
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("applicationdefinition-controller").
		Watches(
			&cozyv1alpha1.ApplicationDefinition{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				r.mu.Lock()
				r.lastEvent = time.Now()
				r.mu.Unlock()
				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Namespace: "cozy-system",
						Name:      "cozystack-api",
					},
				}}
			}),
		).
		Complete(r)
}

type appDefHashView struct {
	Name string                                 `json:"name"`
	Spec cozyv1alpha1.ApplicationDefinitionSpec `json:"spec"`
	// Annotations holds the release.cozystack.io/* subset of the definition's
	// metadata annotations. They belong in the hash because cozystack-api reads
	// them once, at start-up (buildResourceFromCRD in pkg/cmd/server/start.go),
	// and this rollout is the only thing that delivers a change in one: a
	// definition whose whole change is an annotation would otherwise leave the
	// running api serving the previous behaviour until its pod restarted for an
	// unrelated reason. Only that prefix is hashed, because kubectl and Helm
	// rewrite annotations of their own on every apply and hashing those would
	// roll the api Deployment on every reconcile.
	Annotations map[string]string `json:"annotations,omitempty"`
}

// releaseAnnotations returns the release.cozystack.io/* annotations of a
// definition, or nil when it carries none, so a definition without them hashes
// exactly as it did before this field existed.
func releaseAnnotations(annotations map[string]string) map[string]string {
	var selected map[string]string
	for k, v := range annotations {
		if !strings.HasPrefix(k, config.ReleaseAnnotationPrefix) {
			continue
		}
		if selected == nil {
			selected = make(map[string]string)
		}
		selected[k] = v
	}
	return selected
}

func (r *ApplicationDefinitionReconciler) computeConfigHash(ctx context.Context) (string, error) {
	list := &cozyv1alpha1.ApplicationDefinitionList{}
	if err := r.List(ctx, list); err != nil {
		return "", err
	}

	slices.SortFunc(list.Items, sortAppDefs)

	views := make([]appDefHashView, 0, len(list.Items))
	for i := range list.Items {
		views = append(views, appDefHashView{
			Name:        list.Items[i].Name,
			Spec:        list.Items[i].Spec,
			Annotations: releaseAnnotations(list.Items[i].Annotations),
		})
	}
	b, err := json.Marshal(views)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func (r *ApplicationDefinitionReconciler) debouncedRestart(ctx context.Context) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	r.mu.Lock()
	le := r.lastEvent
	lh := r.lastHandled
	debounce := r.Debounce
	r.mu.Unlock()

	if debounce <= 0 {
		debounce = 5 * time.Second
	}
	if le.IsZero() {
		return ctrl.Result{}, nil
	}
	if d := time.Since(le); d < debounce {
		return ctrl.Result{RequeueAfter: debounce - d}, nil
	}
	if !lh.Before(le) {
		return ctrl.Result{}, nil
	}

	newHash, err := r.computeConfigHash(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	tpl, obj, patch, err := r.getWorkload(ctx, types.NamespacedName{Namespace: "cozy-system", Name: "cozystack-api"})
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	oldHash := tpl.Annotations["cozystack.io/config-hash"]

	if oldHash == newHash && oldHash != "" {
		r.mu.Lock()
		r.lastHandled = le
		r.mu.Unlock()
		logger.Info("No changes in ApplicationDefinition config; skipping restart", "hash", newHash)
		return ctrl.Result{}, nil
	}

	tpl.Annotations["cozystack.io/config-hash"] = newHash

	if err := r.Patch(ctx, obj, patch); err != nil {
		return ctrl.Result{}, err
	}

	r.mu.Lock()
	r.lastHandled = le
	r.mu.Unlock()

	logger.Info("Updated cozystack-api podTemplate config-hash; rollout triggered",
		"old", oldHash, "new", newHash)
	return ctrl.Result{}, nil
}

func (r *ApplicationDefinitionReconciler) getWorkload(
	ctx context.Context,
	key types.NamespacedName,
) (tpl *corev1.PodTemplateSpec, obj client.Object, patch client.Patch, err error) {
	dep := &appsv1.Deployment{}
	if err := r.Get(ctx, key, dep); err != nil {
		return nil, nil, nil, err
	}
	obj = dep
	tpl = &dep.Spec.Template
	patch = client.MergeFrom(dep.DeepCopy())
	if tpl.Annotations == nil {
		tpl.Annotations = make(map[string]string)
	}
	return tpl, obj, patch, nil
}

func sortAppDefs(a, b cozyv1alpha1.ApplicationDefinition) int {
	if a.Name == b.Name {
		return 0
	}
	if a.Name < b.Name {
		return -1
	}
	return 1
}
