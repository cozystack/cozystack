package lineagecontrollerwebhook

import (
	"fmt"
	"strings"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
)

type appRef struct {
	group string
	kind  string
}

type runtimeConfig struct {
	appCRDMap map[appRef]*cozyv1alpha1.CozystackResourceDefinition
}

func (l *LineageControllerWebhook) initConfig() {
	l.initOnce.Do(func() {
		if l.config.Load() == nil {
			l.config.Store(&runtimeConfig{
				appCRDMap: make(map[appRef]*cozyv1alpha1.CozystackResourceDefinition),
			})
		}
	})
}

// getApplicationLabel safely extracts an application label from HelmRelease
func getApplicationLabel(hr *helmv2.HelmRelease, key string) (string, error) {
	if hr.Labels == nil {
		return "", fmt.Errorf("cannot map helm release %s/%s to dynamic app: labels are nil", hr.Namespace, hr.Name)
	}
	val, ok := hr.Labels[key]
	if !ok {
		return "", fmt.Errorf("cannot map helm release %s/%s to dynamic app: missing %s label", hr.Namespace, hr.Name, key)
	}
	return val, nil
}

func (l *LineageControllerWebhook) Map(hr *helmv2.HelmRelease) (string, string, string, error) {
	// Extract application metadata from labels
	appKind, err := getApplicationLabel(hr, "apps.cozystack.io/application.kind")
	if err != nil {
		return "", "", "", err
	}
	
	appGroup, err := getApplicationLabel(hr, "apps.cozystack.io/application.group")
	if err != nil {
		return "", "", "", err
	}
	
	appName, err := getApplicationLabel(hr, "apps.cozystack.io/application.name")
	if err != nil {
		return "", "", "", err
	}
	
	// Construct API version from group
	apiVersion := fmt.Sprintf("%s/v1alpha1", appGroup)
	
	// Extract prefix from HelmRelease name by removing the application name
	// HelmRelease name format: <prefix><application-name>
	prefix := strings.TrimSuffix(hr.Name, appName)
	
	// Validate the derived prefix
	// This ensures correctness when appName appears multiple times in hr.Name
	if prefix+appName != hr.Name {
		return "", "", "", fmt.Errorf("cannot derive prefix from helm release %s/%s: name does not end with application name %s", hr.Namespace, hr.Name, appName)
	}
	
	return apiVersion, appKind, prefix, nil
}
