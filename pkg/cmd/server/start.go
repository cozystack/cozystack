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
	"time"

	v1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	"github.com/cozystack/cozystack/pkg/apiserver"
	"github.com/cozystack/cozystack/pkg/config"
	sampleopenapi "github.com/cozystack/cozystack/pkg/generated/openapi"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apiserver/pkg/endpoints/openapi"
	genericapiserver "k8s.io/apiserver/pkg/server"
	genericoptions "k8s.io/apiserver/pkg/server/options"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	basecompatibility "k8s.io/component-base/compatibility"
	baseversion "k8s.io/component-base/version"
	netutils "k8s.io/utils/net"
	"sigs.k8s.io/controller-runtime/pkg/client"
	k8sconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
)

// CozyServerOptions holds the state for the Cozy API server
type CozyServerOptions struct {
	RecommendedOptions *genericoptions.RecommendedOptions

	StdOut io.Writer
	StdErr io.Writer

	AlternateDNS []string
	Client       client.Client

	// Add a field to store the configuration
	ResourceConfig *config.ResourceConfig
}

// NewCozyServerOptions returns a new instance of CozyServerOptions
func NewCozyServerOptions(out, errOut io.Writer) *CozyServerOptions {
	o := &CozyServerOptions{
		RecommendedOptions: genericoptions.NewRecommendedOptions(
			"",
			apiserver.Codecs.LegacyCodec(
				corev1alpha1.SchemeGroupVersion,
				appsv1alpha1.SchemeGroupVersion,
			),
		),
		StdOut: out,
		StdErr: errOut,
	}
	o.RecommendedOptions.Etcd = nil
	return o
}

// NewCommandStartCozyServer provides a CLI handler for the 'start apps-server' command
func NewCommandStartCozyServer(ctx context.Context, defaults *CozyServerOptions) *cobra.Command {
	o := *defaults
	cmd := &cobra.Command{
		Short: "Launch an Cozystack API server",
		Long:  "Launch an Cozystack API server",
		RunE: func(c *cobra.Command, args []string) error {
			if err := o.Complete(); err != nil {
				return err
			}
			if err := o.Validate(args); err != nil {
				return err
			}
			if err := o.RunCozyServer(c.Context()); err != nil {
				return err
			}
			return nil
		},
	}
	cmd.SetContext(ctx)

	flags := cmd.Flags()
	o.RecommendedOptions.AddFlags(flags)

	// Note: KEP-4330 component versioning functionality (k8s.io/apiserver/pkg/util/version)
	// is not available in Kubernetes v0.34.1. The component versioning code has been removed.

	return cmd
}

// Complete fills in the fields that are not set
func (o *CozyServerOptions) Complete() error {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("failed to register types: %w", err)
	}

	cfg, err := k8sconfig.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	o.Client, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("client initialization failed: %w", err)
	}

	crdList := &v1alpha1.CozystackResourceDefinitionList{}

	// Retry with exponential backoff for at least 30 minutes
	const maxRetryDuration = 30 * time.Minute
	const initialDelay = time.Second
	const maxDelay = 2 * time.Minute

	startTime := time.Now()
	delay := initialDelay

	for {
		err := o.Client.List(context.Background(), crdList)
		if err == nil {
			break
		}

		// Check if we've exceeded the maximum retry duration
		if time.Since(startTime) >= maxRetryDuration {
			return fmt.Errorf("failed to list CozystackResourceDefinitions after %v: %w", maxRetryDuration, err)
		}

		// Log the error and wait before retrying
		fmt.Printf("Failed to list CozystackResourceDefinitions (retrying in %v): %v\n", delay, err)
		time.Sleep(delay)

		delay = time.Duration(float64(delay) * 1.5)
		if delay > maxDelay {
			delay = maxDelay
		}
	}

	// Convert to ResourceConfig
	o.ResourceConfig = &config.ResourceConfig{}
	for _, crd := range crdList.Items {
		resource := config.Resource{
			Application: config.ApplicationConfig{
				Kind:          crd.Spec.Application.Kind,
				Singular:      crd.Spec.Application.Singular,
				Plural:        crd.Spec.Application.Plural,
				ShortNames:    []string{}, // TODO: implement shortnames
				OpenAPISchema: crd.Spec.Application.OpenAPISchema,
			},
			Release: config.ReleaseConfig{
				Prefix: crd.Spec.Release.Prefix,
				Labels: crd.Spec.Release.Labels,
				Chart: config.ChartConfig{
					Name: crd.Spec.Release.Chart.Name,
					SourceRef: config.SourceRefConfig{
						Kind:      crd.Spec.Release.Chart.SourceRef.Kind,
						Name:      crd.Spec.Release.Chart.SourceRef.Name,
						Namespace: crd.Spec.Release.Chart.SourceRef.Namespace,
					},
				},
			},
		}
		o.ResourceConfig.Resources = append(o.ResourceConfig.Resources, resource)
	}

	return nil
}

// Validate checks the correctness of the options
func (o CozyServerOptions) Validate(args []string) error {
	var allErrors []error
	allErrors = append(allErrors, o.RecommendedOptions.Validate()...)
	return utilerrors.NewAggregate(allErrors)
}

// Config returns the configuration for the API server based on CozyServerOptions
func (o *CozyServerOptions) Config() (*apiserver.Config, error) {
	// TODO: set the "real" external address
	if err := o.RecommendedOptions.SecureServing.MaybeDefaultWithSelfSignedCerts(
		"localhost", o.AlternateDNS, []net.IP{netutils.ParseIPSloppy("127.0.0.1")},
	); err != nil {
		return nil, fmt.Errorf("error creating self-signed certificates: %v", err)
	}

	// Register *compile-time* resources first.
	corev1alpha1.RegisterStaticTypes(apiserver.Scheme)

	// Register *run-time* resources (from the user’s config file).
	err := appsv1alpha1.RegisterDynamicTypes(apiserver.Scheme, o.ResourceConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to register dynamic types: %v", err)
	}

	serverConfig := genericapiserver.NewRecommendedConfig(apiserver.Codecs)

	serverConfig.OpenAPIConfig = genericapiserver.DefaultOpenAPIConfig(
		sampleopenapi.GetOpenAPIDefinitions, openapi.NewDefinitionNamer(apiserver.Scheme),
	)

	apiVersion := "0.1"
	if o.ResourceConfig != nil {
		raw, err := json.Marshal(o.ResourceConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal resource config: %v", err)
		}
		sum := sha256.Sum256(raw)
		apiVersion = "0.1-" + hex.EncodeToString(sum[:8])
	}

	// capture schemas from config once for fast lookup inside the closure
	kindSchemas := map[string]string{}
	for _, r := range o.ResourceConfig.Resources {
		kindSchemas[r.Application.Kind] = r.Application.OpenAPISchema
	}

	serverConfig.OpenAPIConfig.Info.Title = "Cozy"
	serverConfig.OpenAPIConfig.Info.Version = apiVersion
	serverConfig.OpenAPIConfig.PostProcessSpec = buildPostProcessV2(kindSchemas)

	serverConfig.OpenAPIV3Config = genericapiserver.DefaultOpenAPIV3Config(
		sampleopenapi.GetOpenAPIDefinitions, openapi.NewDefinitionNamer(apiserver.Scheme),
	)
	serverConfig.OpenAPIV3Config.Info.Title = "Cozy"
	serverConfig.OpenAPIV3Config.Info.Version = apiVersion

	serverConfig.OpenAPIV3Config.PostProcessSpec = buildPostProcessV3(kindSchemas)

	// Set FeatureGate and EffectiveVersion - required for Complete() in Kubernetes v0.34.1
	// Following the pattern from sample-apiserver, but creating EffectiveVersion directly
	// without ComponentGlobalsRegistry
	serverConfig.FeatureGate = utilfeature.DefaultMutableFeatureGate
	// Create EffectiveVersion directly using compatibility package
	// This is needed even without ComponentGlobalsRegistry
	if baseversion.DefaultKubeBinaryVersion != "" {
		serverConfig.EffectiveVersion = basecompatibility.NewEffectiveVersionFromString(baseversion.DefaultKubeBinaryVersion, "", "")
	}

	if err := o.RecommendedOptions.ApplyTo(serverConfig); err != nil {
		return nil, err
	}

	config := &apiserver.Config{
		GenericConfig:  serverConfig,
		ResourceConfig: o.ResourceConfig,
	}
	return config, nil
}

// RunCozyServer launches a new CozyServer based on CozyServerOptions
func (o CozyServerOptions) RunCozyServer(ctx context.Context) error {
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
