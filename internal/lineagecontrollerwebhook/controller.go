package lineagecontrollerwebhook

import (
	"context"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// +kubebuilder:rbac:groups=cozystack.io,resources=cozystackresourcedefinitions,verbs=list;watch;get

func (c *LineageControllerWebhook) SetupWithManagerAsController(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cozyv1alpha1.CozystackResourceDefinition{}).
		Complete(c)
}

func (c *LineageControllerWebhook) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	crds := &cozyv1alpha1.CozystackResourceDefinitionList{}
	if err := c.List(ctx, crds); err != nil {
		l.Error(err, "failed reading CozystackResourceDefinitions")
		return ctrl.Result{}, err
	}
	cfg := &runtimeConfig{
		chartAppMap: make(map[chartRef]*cozyv1alpha1.CozystackResourceDefinition),
		appCRDMap:   make(map[appRef]*cozyv1alpha1.CozystackResourceDefinition),
	}
	for _, crd := range crds.Items {
		var chRef chartRef
		if crd.Spec.Release.Chart != nil {
			// Using chart (HelmRepository)
			chRef = chartRef{
				repo:       crd.Spec.Release.Chart.SourceRef.Name,
				chart:      crd.Spec.Release.Chart.Name,
				isChartRef: false,
			}
		} else if crd.Spec.Release.ChartRef != nil {
			// Using chartRef (ExternalArtifact)
			chRef = chartRef{
				repo:       "",
				chart:      crd.Spec.Release.ChartRef.SourceRef.Name,
				isChartRef: true,
				namespace:  crd.Spec.Release.ChartRef.SourceRef.Namespace,
			}
		} else {
			l.Info("CozystackResourceDefinition has neither chart nor chartRef, skipping", "name", crd.Name)
			continue
		}
		
		appRef := appRef{
			"apps.cozystack.io",
			crd.Spec.Application.Kind,
		}

		newRef := crd
		if _, exists := cfg.chartAppMap[chRef]; exists {
			l.Info("duplicate chart mapping detected; ignoring subsequent entry", "key", chRef)
		} else {
			cfg.chartAppMap[chRef] = &newRef
		}
		if _, exists := cfg.appCRDMap[appRef]; exists {
			l.Info("duplicate app mapping detected; ignoring subsequent entry", "key", appRef)
		} else {
			cfg.appCRDMap[appRef] = &newRef
		}
	}
	c.config.Store(cfg)
	return ctrl.Result{}, nil
}
