package lineagecontrollerwebhook

import (
	"context"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/internal/shared/appdefowner"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// +kubebuilder:rbac:groups=cozystack.io,resources=applicationdefinitions,verbs=list;watch;get

func (c *LineageControllerWebhook) SetupWithManagerAsController(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cozyv1alpha1.ApplicationDefinition{}).
		Complete(c)
}

func (c *LineageControllerWebhook) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	crds := &cozyv1alpha1.ApplicationDefinitionList{}
	if err := c.List(ctx, crds); err != nil {
		l.Error(err, "failed reading ApplicationDefinitions")
		return ctrl.Result{}, err
	}
	cfg := &runtimeConfig{
		appCRDMap: make(map[appRef]*cozyv1alpha1.ApplicationDefinition),
	}
	owners := appdefowner.Owners(crds.Items)
	for _, crd := range crds.Items {
		appRef := appRef{
			"apps.cozystack.io",
			crd.Spec.Application.Kind,
		}

		if crd.Spec.Application.Kind == "" {
			continue
		}
		if owners[crd.Spec.Application.Kind] != crd.Name {
			l.Info("duplicate app mapping detected; the kind is owned by another definition", "key", appRef, "ignored", crd.Name, "owner", owners[crd.Spec.Application.Kind])
			continue
		}
		newRef := crd
		cfg.appCRDMap[appRef] = &newRef
	}
	c.config.Store(cfg)
	return ctrl.Result{}, nil
}
