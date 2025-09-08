package lineagecontrollerwebhook

import (
	"fmt"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
)

type chartRef struct {
	repo  string
	chart string
}

type appRef struct {
	group string
	kind  string
}

type runtimeConfig struct {
	chartAppMap map[chartRef]*cozyv1alpha1.CozystackResourceDefinition
	appCRDMap   map[appRef]*cozyv1alpha1.CozystackResourceDefinition
}

func (l *LineageControllerWebhook) initConfig() {
	l.initOnce.Do(func() {
		if l.config.Load() == nil {
			l.config.Store(&runtimeConfig{chartAppMap: make(map[chartRef]*cozyv1alpha1.CozystackResourceDefinition)})
		}
	})
}

func (l *LineageControllerWebhook) Map(hr *helmv2.HelmRelease) (string, string, string, error) {
	cfg, ok := l.config.Load().(*runtimeConfig)
	if !ok {
		return "", "", "", fmt.Errorf("failed to load chart-app mapping from config")
	}
	s := hr.Spec.Chart.Spec
	val, ok := cfg.chartAppMap[chartRef{s.SourceRef.Name, s.Chart}]
	if !ok {
		return "", "", "", fmt.Errorf("cannot map helm release %s/%s to dynamic app", hr.Namespace, hr.Name)
	}
	return "apps.cozystack.io/v1alpha1", val.Spec.Application.Kind, val.Spec.Release.Prefix, nil
}
