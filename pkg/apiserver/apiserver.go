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
	"fmt"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"

	"github.com/cozystack/cozystack/pkg/apis/apps"
	appsinstall "github.com/cozystack/cozystack/pkg/apis/apps/install"
	coreinstall "github.com/cozystack/cozystack/pkg/apis/apps/install"
	"github.com/cozystack/cozystack/pkg/apis/core"
	"github.com/cozystack/cozystack/pkg/config"
	cozyregistry "github.com/cozystack/cozystack/pkg/registry"
	applicationstorage "github.com/cozystack/cozystack/pkg/registry/apps/application"
	tenantnamespacestorage "github.com/cozystack/cozystack/pkg/registry/core/tenantnamespace"
	tenantsecretstorage "github.com/cozystack/cozystack/pkg/registry/core/tenantsecret"
)

var (
	// Scheme defines methods for serializing and deserializing API objects.
	Scheme = runtime.NewScheme()
	// Codecs provides methods for retrieving codecs and serializers for specific
	// versions and content types.
	Codecs            = serializer.NewCodecFactory(Scheme)
	CozyComponentName = "cozy"
)

func init() {
	appsinstall.Install(Scheme)
	coreinstall.Install(Scheme)

	// Register HelmRelease types.
	if err := helmv2.AddToScheme(Scheme); err != nil {
		panic(fmt.Sprintf("Failed to add HelmRelease types to scheme: %v", err))
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
	inClusterConfig, err := restclient.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("unable to get in-cluster config: %v", err)
	}

	dynamicClient, err := dynamic.NewForConfig(inClusterConfig)
	if err != nil {
		return nil, fmt.Errorf("unable to create dynamic client: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(inClusterConfig)
	if err != nil {
		return nil, fmt.Errorf("create kube clientset: %v", err)
	}

	// --- static, cluster-scoped resource for core group ---
	coreV1alpha1Storage := map[string]rest.Storage{}
	coreV1alpha1Storage["tenantnamespaces"] = cozyregistry.RESTInPeace(
		tenantnamespacestorage.NewREST(
			clientset.CoreV1(),
			clientset.AuthorizationV1(),
			20,
		),
	)
	coreV1alpha1Storage["tenantsecrets"] = cozyregistry.RESTInPeace(
		tenantsecretstorage.NewREST(
			clientset.CoreV1(),
		),
	)

	coreApiGroupInfo := genericapiserver.NewDefaultAPIGroupInfo(core.GroupName, Scheme, metav1.ParameterCodec, Codecs)
	coreApiGroupInfo.VersionedResourcesStorageMap["v1alpha1"] = coreV1alpha1Storage
	if err := s.GenericAPIServer.InstallAPIGroup(&coreApiGroupInfo); err != nil {
		return nil, err
	}

	// --- dynamically-configured, per-tenant resources ---
	appsV1alpha1Storage := map[string]rest.Storage{}
	for _, resConfig := range c.ResourceConfig.Resources {
		storage := applicationstorage.NewREST(dynamicClient, &resConfig)
		appsV1alpha1Storage[resConfig.Application.Plural] = cozyregistry.RESTInPeace(storage)
	}
	appsApiGroupInfo := genericapiserver.NewDefaultAPIGroupInfo(apps.GroupName, Scheme, metav1.ParameterCodec, Codecs)
	appsApiGroupInfo.VersionedResourcesStorageMap["v1alpha1"] = appsV1alpha1Storage
	if err := s.GenericAPIServer.InstallAPIGroup(&appsApiGroupInfo); err != nil {
		return nil, err
	}

	return s, nil
}
