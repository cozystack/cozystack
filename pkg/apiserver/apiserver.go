/*
Copyright 2024 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package apiserver

import (
	"context"
	"fmt"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/cozystack/cozystack/pkg/apis/apps"
	appsinstall "github.com/cozystack/cozystack/pkg/apis/apps/install"
	"github.com/cozystack/cozystack/pkg/apis/core"
	coreinstall "github.com/cozystack/cozystack/pkg/apis/core/install"
	"github.com/cozystack/cozystack/pkg/config"
	cozyregistry "github.com/cozystack/cozystack/pkg/registry"
	applicationstorage "github.com/cozystack/cozystack/pkg/registry/apps/application"
	tenantmodulestorage "github.com/cozystack/cozystack/pkg/registry/core/tenantmodule"
	tenantnamespacestorage "github.com/cozystack/cozystack/pkg/registry/core/tenantnamespace"
	tenantsecretstorage "github.com/cozystack/cozystack/pkg/registry/core/tenantsecret"
)

var (
	// Scheme defines methods for serializing and deserializing API objects.
	Scheme    = runtime.NewScheme()
	mgrScheme = runtime.NewScheme()
	// Codecs provides methods for retrieving codecs and serializers for specific
	// versions and content types.
	Codecs            = serializer.NewCodecFactory(Scheme)
	CozyComponentName = "cozy"
	syncPeriod        = 5 * time.Minute
)

func init() {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{
		Development: true,
		// any other zap.Options tweaks
	})))
	klog.SetLogger(ctrl.Log.WithName("klog"))
	appsinstall.Install(Scheme)
	coreinstall.Install(Scheme)

	// Register HelmRelease types.
	if err := helmv2.AddToScheme(mgrScheme); err != nil {
		panic(fmt.Errorf("Failed to add HelmRelease types to scheme: %w", err))
	}

	if err := corev1.AddToScheme(mgrScheme); err != nil {
		panic(fmt.Errorf("Failed to add core types to scheme: %w", err))
	}
	if err := rbacv1.AddToScheme(mgrScheme); err != nil {
		panic(fmt.Errorf("Failed to add RBAC types to scheme: %w", err))
	}
	// Add unversioned types.
	metav1.AddToGroupVersion(Scheme, schema.GroupVersion{Version: "v1"})

	// Add unversioned types.
	unversioned := schema.GroupVersion{Group: "", Version: "v1"}
	Scheme.AddUnversionedTypes(unversioned,
		&metav1.Status{},
		&metav1.APIVersions{},
		&metav1.APIGroupList{},
		&metav1.APIGroup{},
		&metav1.APIResourceList{},
	)
}

// Config defines the configuration for the apiserver.
type Config struct {
	GenericConfig  *genericapiserver.RecommendedConfig
	ResourceConfig *config.ResourceConfig
}

// CozyServer holds the state for the Kubernetes master/api server.
type CozyServer struct {
	GenericAPIServer *genericapiserver.GenericAPIServer
}

type completedConfig struct {
	GenericConfig  genericapiserver.CompletedConfig
	ResourceConfig *config.ResourceConfig
}

// CompletedConfig embeds a private pointer that cannot be created outside of this package.
type CompletedConfig struct {
	*completedConfig
}

// Complete fills in any fields that are not set but are required for valid operation.
func (cfg *Config) Complete() CompletedConfig {
	c := completedConfig{
		cfg.GenericConfig.Complete(),
		cfg.ResourceConfig,
	}

	return CompletedConfig{&c}
}

// New returns a new instance of CozyServer from the given configuration.
func (c completedConfig) New() (*CozyServer, error) {
	genericServer, err := c.GenericConfig.New("cozy-apiserver", genericapiserver.NewEmptyDelegate())
	if err != nil {
		return nil, err
	}

	s := &CozyServer{
		GenericAPIServer: genericServer,
	}

	// Create a dynamic client for HelmRelease using InClusterConfig.
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: mgrScheme,
		Cache:  cache.Options{SyncPeriod: &syncPeriod},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build manager: %w", err)
	}

	ctx := ctrl.SetupSignalHandler()

	if err = mustGetInformers(ctx, mgr,
		&helmv2.HelmRelease{},
		&corev1.Secret{},
		&corev1.Namespace{},
		&corev1.Service{},
		&rbacv1.RoleBinding{},
	); err != nil {
		return nil, fmt.Errorf("failed to get informers: %w", err)
	}

	go func() {
		if err := mgr.Start(ctx); err != nil {
			panic(fmt.Errorf("manager start failed: %w", err))
		}
	}()

	if ok := mgr.GetCache().WaitForCacheSync(ctx); !ok {
		return nil, fmt.Errorf("cache sync failed")
	}

	cli := mgr.GetClient()
	watchCli, err := client.NewWithWatch(cfg, client.Options{Scheme: mgrScheme})
	if err != nil {
		return nil, fmt.Errorf("failed to build watch client: %w", err)
	}
	// --- static, cluster-scoped resource for core group ---
	coreV1alpha1Storage := map[string]rest.Storage{}
	coreV1alpha1Storage["tenantnamespaces"] = cozyregistry.RESTInPeace(
		tenantnamespacestorage.NewREST(cli, watchCli),
	)
	coreV1alpha1Storage["tenantsecrets"] = cozyregistry.RESTInPeace(
		tenantsecretstorage.NewREST(cli, watchCli),
	)
	coreV1alpha1Storage["tenantmodules"] = cozyregistry.RESTInPeace(
		tenantmodulestorage.NewREST(cli, watchCli),
	)

	coreApiGroupInfo := genericapiserver.NewDefaultAPIGroupInfo(core.GroupName, Scheme, metav1.ParameterCodec, Codecs)
	coreApiGroupInfo.VersionedResourcesStorageMap["v1alpha1"] = coreV1alpha1Storage
	if err := s.GenericAPIServer.InstallAPIGroup(&coreApiGroupInfo); err != nil {
		return nil, err
	}

	// --- dynamically-configured, per-tenant resources ---
	appsV1alpha1Storage := map[string]rest.Storage{}
	for _, resConfig := range c.ResourceConfig.Resources {
		storage := applicationstorage.NewREST(cli, watchCli, &resConfig)
		appsV1alpha1Storage[resConfig.Application.Plural] = cozyregistry.RESTInPeace(storage)
	}
	appsApiGroupInfo := genericapiserver.NewDefaultAPIGroupInfo(apps.GroupName, Scheme, metav1.ParameterCodec, Codecs)
	appsApiGroupInfo.VersionedResourcesStorageMap["v1alpha1"] = appsV1alpha1Storage
	if err := s.GenericAPIServer.InstallAPIGroup(&appsApiGroupInfo); err != nil {
		return nil, err
	}

	return s, nil
}

func mustGetInformers(ctx context.Context, mgr ctrl.Manager, types ...client.Object) error {
	for i := range types {
		if _, err := mgr.GetCache().GetInformer(ctx, types[i]); err != nil {
			return fmt.Errorf("failed to get informer for %T: %w", types[i], err)
		}
	}
	return nil
}
