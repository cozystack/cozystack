package lineagecontrollerwebhook

import (
	"fmt"
	"strings"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
)

func (l *LineageControllerWebhook) initConfig() {
	// No longer needed - we use labels directly from HelmRelease
}

func (l *LineageControllerWebhook) Map(hr *helmv2.HelmRelease) (string, string, string, error) {
	// Extract application metadata from labels
	appKind, ok := hr.Labels["apps.cozystack.io/application.kind"]
	if !ok {
		return "", "", "", fmt.Errorf("cannot map helm release %s/%s to dynamic app: missing apps.cozystack.io/application.kind label", hr.Namespace, hr.Name)
	}
	
	appGroup, ok := hr.Labels["apps.cozystack.io/application.group"]
	if !ok {
		return "", "", "", fmt.Errorf("cannot map helm release %s/%s to dynamic app: missing apps.cozystack.io/application.group label", hr.Namespace, hr.Name)
	}
	
	appName, ok := hr.Labels["apps.cozystack.io/application.name"]
	if !ok {
		return "", "", "", fmt.Errorf("cannot map helm release %s/%s to dynamic app: missing apps.cozystack.io/application.name label", hr.Namespace, hr.Name)
	}
	
	// Construct API version from group
	apiVersion := fmt.Sprintf("%s/v1alpha1", appGroup)
	
	// Extract prefix from HelmRelease name by removing the application name
	// HelmRelease name format: <prefix><application-name>
	prefix := strings.TrimSuffix(hr.Name, appName)
	
	return apiVersion, appKind, prefix, nil
}
