package lineage

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

var (
	dynClient dynamic.Interface
	mapper    meta.RESTMapper
	l         logr.Logger
	ctx       context.Context
)

func init() {
	cfg := config.GetConfigOrDie()

	dynClient, _ = dynamic.NewForConfig(cfg)

	discoClient, _ := discovery.NewDiscoveryClientForConfig(cfg)

	cachedDisco := memory.NewMemCacheClient(discoClient)
	mapper = restmapper.NewDeferredDiscoveryRESTMapper(cachedDisco)

	zapLogger, _ := zap.NewDevelopment()
	l = zapr.NewLogger(zapLogger)
	ctx = logr.NewContext(context.Background(), l)
}

// labelsMapper implements AppMapper using HelmRelease labels.
type labelsMapper struct{}

func (m *labelsMapper) Map(hr *helmv2.HelmRelease) (string, string, string, error) {
	if hr.Labels == nil {
		return "", "", "", fmt.Errorf("cannot map helm release %s/%s: labels are nil", hr.Namespace, hr.Name)
	}

	appKind, ok := hr.Labels["apps.cozystack.io/application.kind"]
	if !ok {
		return "", "", "", fmt.Errorf("cannot map helm release %s/%s: missing application.kind label", hr.Namespace, hr.Name)
	}

	appGroup, ok := hr.Labels["apps.cozystack.io/application.group"]
	if !ok {
		return "", "", "", fmt.Errorf("cannot map helm release %s/%s: missing application.group label", hr.Namespace, hr.Name)
	}

	appName, ok := hr.Labels["apps.cozystack.io/application.name"]
	if !ok {
		return "", "", "", fmt.Errorf("cannot map helm release %s/%s: missing application.name label", hr.Namespace, hr.Name)
	}

	apiVersion := fmt.Sprintf("%s/v1alpha1", appGroup)
	prefix := strings.TrimSuffix(hr.Name, appName)

	return apiVersion, appKind, prefix, nil
}

func TestWalkingOwnershipGraph(t *testing.T) {
	obj, err := dynClient.Resource(schema.GroupVersionResource{"", "v1", "pods"}).Namespace(os.Args[1]).Get(ctx, os.Args[2], metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	nodes := WalkOwnershipGraph(ctx, dynClient, mapper, &labelsMapper{}, obj)
	for _, node := range nodes {
		fmt.Printf("%#v\n", node)
	}
}
