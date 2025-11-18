package lineagecontrollerwebhook

import (
	"fmt"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
)

type chartRef struct {
	// For chart (HelmRepository): repo is SourceRef.Name, chart is Chart.Name
	// For chartRef (ExternalArtifact): repo is empty, chart is SourceRef.Name
	repo  string
	chart string
	// isChartRef indicates if this is a chartRef (ExternalArtifact) reference
	isChartRef bool
	// namespace is used for chartRef (ExternalArtifact)
	namespace string
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
			l.config.Store(&runtimeConfig{
				chartAppMap: make(map[chartRef]*cozyv1alpha1.CozystackResourceDefinition),
				appCRDMap:   make(map[appRef]*cozyv1alpha1.CozystackResourceDefinition),
			})
		}
	})
}

func (l *LineageControllerWebhook) Map(hr *helmv2.HelmRelease) (string, string, string, error) {
	cfg, ok := l.config.Load().(*runtimeConfig)
	if !ok {
		return "", "", "", fmt.Errorf("failed to load chart-app mapping from config")
	}
	
	var chRef chartRef
	if hr.Spec.Chart != nil {
		// Using chart (HelmRepository)
		s := hr.Spec.Chart.Spec
		chRef = chartRef{
			repo:       s.SourceRef.Name,
			chart:      s.Chart,
			isChartRef: false,
		}
	} else if hr.Spec.ChartRef != nil {
		// Using chartRef (ExternalArtifact)
		chRef = chartRef{
			repo:       "",
			chart:      hr.Spec.ChartRef.Name,
			isChartRef: true,
			namespace:  hr.Spec.ChartRef.Namespace,
		}
	} else {
		return "", "", "", fmt.Errorf("cannot map helm release %s/%s to dynamic app: neither chart nor chartRef is set", hr.Namespace, hr.Name)
	}
	
	val, ok := cfg.chartAppMap[chRef]
	if !ok {
		return "", "", "", fmt.Errorf("cannot map helm release %s/%s to dynamic app", hr.Namespace, hr.Name)
	}
	return "apps.cozystack.io/v1alpha1", val.Spec.Application.Kind, val.Spec.Release.Prefix, nil
}
