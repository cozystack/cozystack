package kubeovnplunger

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

var testPlunger *KubeOVNPlunger

func init() {
	scheme := runtime.NewScheme()
	cfg := config.GetConfigOrDie()
	c, _ := client.New(cfg, client.Options{})
	cs, _ := kubernetes.NewForConfig(cfg)
	testPlunger = &KubeOVNPlunger{
		Client:    c,
		Scheme:    scheme,
		ClientSet: cs,
		REST:      cfg,
	}
}

func TestPlungerGetsStatuses(t *testing.T) {
	_, err := testPlunger.Reconcile(context.Background(), ctrl.Request{})
	if err != nil {
		t.Errorf("error should be nil but it's %s", err)
	}
}
