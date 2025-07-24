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

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"

	"github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"github.com/cozystack/cozystack/pkg/apiserver"
	"github.com/cozystack/cozystack/pkg/config"
	sampleopenapi "github.com/cozystack/cozystack/pkg/generated/openapi"
	"github.com/spf13/cobra"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/apiserver/pkg/endpoints/openapi"
	genericapiserver "k8s.io/apiserver/pkg/server"
	genericoptions "k8s.io/apiserver/pkg/server/options"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	utilversionpkg "k8s.io/apiserver/pkg/util/version"
	"k8s.io/component-base/featuregate"
	baseversion "k8s.io/component-base/version"
	netutils "k8s.io/utils/net"
)

// AppsServerOptions holds the state for the Apps API server
type AppsServerOptions struct {
	RecommendedOptions *genericoptions.RecommendedOptions

	StdOut io.Writer
	StdErr io.Writer

	AlternateDNS []string

	// Add a field to store the configuration path
	ResourceConfigPath string

	// Add a field to store the configuration
	ResourceConfig *config.ResourceConfig
}

// NewAppsServerOptions returns a new instance of AppsServerOptions
func NewAppsServerOptions(out, errOut io.Writer) *AppsServerOptions {
	o := &AppsServerOptions{
		RecommendedOptions: genericoptions.NewRecommendedOptions(
			"",
			apiserver.Codecs.LegacyCodec(v1alpha1.SchemeGroupVersion),
		),

		StdOut: out,
		StdErr: errOut,
	}
	o.RecommendedOptions.Etcd = nil
	return o
}

// NewCommandStartAppsServer provides a CLI handler for the 'start apps-server' command
func NewCommandStartAppsServer(ctx context.Context, defaults *AppsServerOptions) *cobra.Command {
	o := *defaults
	cmd := &cobra.Command{
		Short: "Launch an Apps API server",
		Long:  "Launch an Apps API server",
		PersistentPreRunE: func(*cobra.Command, []string) error {
			return utilversionpkg.DefaultComponentGlobalsRegistry.Set()
		},
		RunE: func(c *cobra.Command, args []string) error {
			if err := o.Complete(); err != nil {
				return err
			}
			if err := o.Validate(args); err != nil {
				return err
			}
			if err := o.RunAppsServer(c.Context()); err != nil {
				return err
			}
			return nil
		},
	}
	cmd.SetContext(ctx)

	flags := cmd.Flags()
	o.RecommendedOptions.AddFlags(flags)

	// Add a flag for the config path
	flags.StringVar(&o.ResourceConfigPath, "config", "config.yaml", "Path to the resource configuration file")

	// The following lines demonstrate how to configure version compatibility and feature gates
	// for the "Apps" component according to KEP-4330.

	// Create a default version object for the "Apps" component.
	defaultAppsVersion := "1.1"
	// Register the "Apps" component in the global component registry,
	// associating it with its effective version and feature gate configuration.
	_, appsFeatureGate := utilversionpkg.DefaultComponentGlobalsRegistry.ComponentGlobalsOrRegister(
		apiserver.AppsComponentName, utilversionpkg.NewEffectiveVersion(defaultAppsVersion),
		featuregate.NewVersionedFeatureGate(version.MustParse(defaultAppsVersion)),
	)

	// Add feature gate specifications for the "Apps" component.
	utilruntime.Must(appsFeatureGate.AddVersioned(map[featuregate.Feature]featuregate.VersionedSpecs{
		// Example of adding feature gates:
		// "FeatureName": {{"v1", true}, {"v2", false}},
	}))

	// Register the standard kube component if it is not already registered in the global registry.
	_, _ = utilversionpkg.DefaultComponentGlobalsRegistry.ComponentGlobalsOrRegister(
		utilversionpkg.DefaultKubeComponent,
		utilversionpkg.NewEffectiveVersion(baseversion.DefaultKubeBinaryVersion),
		utilfeature.DefaultMutableFeatureGate,
	)

	// Set the version emulation mapping from the "Apps" component to the kube component.
	utilruntime.Must(utilversionpkg.DefaultComponentGlobalsRegistry.SetEmulationVersionMapping(
		apiserver.AppsComponentName, utilversionpkg.DefaultKubeComponent, AppsVersionToKubeVersion,
	))

	// Add flags from the global component registry.
	utilversionpkg.DefaultComponentGlobalsRegistry.AddFlags(flags)

	return cmd
}

// Complete fills in the fields that are not set
func (o *AppsServerOptions) Complete() error {
	// Load the configuration file
	cfg, err := config.LoadConfig(o.ResourceConfigPath)
	if err != nil {
		return fmt.Errorf("failed to load config from %s: %v", o.ResourceConfigPath, err)
	}
	o.ResourceConfig = cfg
	return nil
}

// Validate checks the correctness of the options
func (o AppsServerOptions) Validate(args []string) error {
	var allErrors []error
	allErrors = append(allErrors, o.RecommendedOptions.Validate()...)
	allErrors = append(allErrors, utilversionpkg.DefaultComponentGlobalsRegistry.Validate()...)
	return utilerrors.NewAggregate(allErrors)
}

// Config returns the configuration for the API server based on AppsServerOptions
func (o *AppsServerOptions) Config() (*apiserver.Config, error) {
	// TODO: set the "real" external address
	if err := o.RecommendedOptions.SecureServing.MaybeDefaultWithSelfSignedCerts(
		"localhost", o.AlternateDNS, []net.IP{netutils.ParseIPSloppy("127.0.0.1")},
	); err != nil {
		return nil, fmt.Errorf("error creating self-signed certificates: %v", err)
	}

	// Register *compile-time* resources first.
	v1alpha1.RegisterStaticTypes(apiserver.Scheme)

	// Register *run-time* resources (from the user’s config file).
	err := v1alpha1.RegisterDynamicTypes(apiserver.Scheme, o.ResourceConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to register dynamic types: %v", err)
	}

	serverConfig := genericapiserver.NewRecommendedConfig(apiserver.Codecs)

	serverConfig.OpenAPIConfig = genericapiserver.DefaultOpenAPIConfig(
		sampleopenapi.GetOpenAPIDefinitions, openapi.NewDefinitionNamer(apiserver.Scheme),
	)

	version := "0.1"
	if o.ResourceConfig != nil {
		raw, err := json.Marshal(o.ResourceConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal resource config: %v", err)
		}
		sum := sha256.Sum256(raw)
		version = "0.1-" + hex.EncodeToString(sum[:8])
	}

	// capture schemas from config once for fast lookup inside the closure
	kindSchemas := map[string]string{}
	for _, r := range o.ResourceConfig.Resources {
		kindSchemas[r.Application.Kind] = r.Application.OpenAPISchema
	}

	serverConfig.OpenAPIConfig.Info.Title = "Apps"
	serverConfig.OpenAPIConfig.Info.Version = version
	serverConfig.OpenAPIConfig.PostProcessSpec = buildPostProcessV2(kindSchemas)

	serverConfig.OpenAPIV3Config = genericapiserver.DefaultOpenAPIV3Config(
		sampleopenapi.GetOpenAPIDefinitions, openapi.NewDefinitionNamer(apiserver.Scheme),
	)
	serverConfig.OpenAPIV3Config.Info.Title = "Apps"
	serverConfig.OpenAPIV3Config.Info.Version = version

	serverConfig.OpenAPIV3Config.PostProcessSpec = buildPostProcessV3(kindSchemas)

	serverConfig.FeatureGate = utilversionpkg.DefaultComponentGlobalsRegistry.FeatureGateFor(
		utilversionpkg.DefaultKubeComponent,
	)
	serverConfig.EffectiveVersion = utilversionpkg.DefaultComponentGlobalsRegistry.EffectiveVersionFor(
		apiserver.AppsComponentName,
	)

	if err := o.RecommendedOptions.ApplyTo(serverConfig); err != nil {
		return nil, err
	}

	config := &apiserver.Config{
		GenericConfig:  serverConfig,
		ResourceConfig: o.ResourceConfig,
	}
	return config, nil
}

// RunAppsServer launches a new AppsServer based on AppsServerOptions
func (o AppsServerOptions) RunAppsServer(ctx context.Context) error {
	config, err := o.Config()
	if err != nil {
		return err
	}

	server, err := config.Complete().New()
	if err != nil {
		return err
	}

	server.GenericAPIServer.AddPostStartHookOrDie("start-sample-server-informers", func(context genericapiserver.PostStartHookContext) error {
		config.GenericConfig.SharedInformerFactory.Start(context.Done())
		return nil
	})

	return server.GenericAPIServer.PrepareRun().RunWithContext(ctx)
}

// AppsVersionToKubeVersion defines the version mapping between the Apps component and kube
func AppsVersionToKubeVersion(ver *version.Version) *version.Version {
	if ver.Major() != 1 {
		return nil
	}
	kubeVer := utilversionpkg.DefaultKubeEffectiveVersion().BinaryVersion()
	// "1.2" corresponds to kubeVer
	offset := int(ver.Minor()) - 2
	mappedVer := kubeVer.OffsetMinor(offset)
	if mappedVer.GreaterThan(kubeVer) {
		return kubeVer
	}
	return mappedVer
}
