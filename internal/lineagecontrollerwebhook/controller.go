package lineagecontrollerwebhook

import (
	ctrl "sigs.k8s.io/controller-runtime"
)

// SetupWithManagerAsController is no longer needed since we don't watch CozystackResourceDefinitions
func (c *LineageControllerWebhook) SetupWithManagerAsController(mgr ctrl.Manager) error {
	// No controller needed - we use labels directly from HelmRelease
	return nil
}
