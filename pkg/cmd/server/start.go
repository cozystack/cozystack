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
	sdnv1alpha1 "github.com/cozystack/cozystack/pkg/apis/sdn/v1alpha1"
	"github.com/cozystack/cozystack/pkg/apiserver"
	"github.com/cozystack/cozystack/pkg/config"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
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

	// Raw HelmRelease generation flag values; parsed and validated in
	// Complete() with config.ParsePositiveDuration so a misconfigured flag
	// fails fast at startup. Kept in sync with the cozystack-operator
	// flags of the same name so both HelmRelease-generating paths share
	// the same retry strategy and cadence.
	HelmReleaseInterval       string
	HelmReleaseRetryInterval  string
	HelmReleaseInstallTimeout string
	HelmReleaseUpgradeTimeout string
	HelmReleaseMaxHistory     int
}

// NewCozyServerOptions returns a new instance of CozyServerOptions
func NewCozyServerOptions(out, errOut io.Writer) *CozyServerOptions {
	o := &CozyServerOptions{
		RecommendedOptions: genericoptions.NewRecommendedOptions(
			"",
			apiserver.Codecs.LegacyCodec(
				corev1alpha1.SchemeGroupVersion,
				appsv1alpha1.SchemeGroupVersion,
				sdnv1alpha1.SchemeGroupVersion,
			),
		),
		StdOut: out,
		StdErr: errOut,
		// Defaults match cozystack-operator: production semantics are
		// identical between the two HelmRelease-generating paths.
		HelmReleaseInterval:       "5m",
		HelmReleaseRetryInterval:  "30s",
		HelmReleaseInstallTimeout: "10m",
		HelmReleaseUpgradeTimeout: "10m",
		HelmReleaseMaxHistory:     5,
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

	// HelmRelease generation knobs. Names, defaults, and validation match
	// the cozystack-operator flags of the same name so both
	// HelmRelease-generating paths can be tuned together.
	flags.StringVar(&o.HelmReleaseInterval, "helmrelease-interval", o.HelmReleaseInterval,
		"Reconcile interval applied to HelmReleases generated from Application resources "+
			"(Spec.Interval). Mirrors the cozystack-operator flag of the same name.")
	flags.StringVar(&o.HelmReleaseRetryInterval, "helmrelease-retry-interval", o.HelmReleaseRetryInterval,
		"Retry interval applied to Install.Strategy and Upgrade.Strategy of HelmReleases generated "+
			"from Application resources. With Strategy.Name=RetryOnFailure, this controls how long "+
			"the controller waits between failed install/upgrade attempts. Decoupled from "+
			"--helmrelease-interval so failures recover fast without polling healthy releases at the "+
			"same cadence.")
	flags.StringVar(&o.HelmReleaseInstallTimeout, "helmrelease-install-timeout", o.HelmReleaseInstallTimeout,
		"Default timeout for the Helm install action of HelmReleases generated from Application "+
			"resources (Spec.Install.Timeout). Overridden per-Application by the "+
			"release.cozystack.io/helm-install-timeout annotation on the ApplicationDefinition; the "+
			"same annotation also overrides --helmrelease-upgrade-timeout.")
	flags.StringVar(&o.HelmReleaseUpgradeTimeout, "helmrelease-upgrade-timeout", o.HelmReleaseUpgradeTimeout,
		"Default timeout for the Helm upgrade action of HelmReleases generated from Application "+
			"resources (Spec.Upgrade.Timeout). Overridden per-Application by the "+
			"release.cozystack.io/helm-install-timeout annotation (which sets both install and "+
			"upgrade), or by release.cozystack.io/helm-upgrade-timeout to override only the upgrade "+
			"side for a kind that needs an asymmetric budget.")
	flags.IntVar(&o.HelmReleaseMaxHistory, "helmrelease-max-history", o.HelmReleaseMaxHistory,
		"Number of release revisions Helm keeps for HelmReleases generated from Application "+
			"resources (Spec.MaxHistory). 0 means unlimited; 5 matches Helm's default.")

	// Note: KEP-4330 component versioning functionality (k8s.io/apiserver/pkg/util/version)
	// is not available in Kubernetes v0.34.1. The component versioning code has been removed.

	return cmd
}

// helmReleaseFlagValues holds the parsed, validated HelmRelease generation
// flags. Populated by parseAndValidateHelmReleaseFlags before any kubernetes
// I/O so a misconfigured server restarts loudly instead of waiting until the
// first Application is created.
type helmReleaseFlagValues struct {
	interval       time.Duration
	retryInterval  time.Duration
	installTimeout time.Duration
	upgradeTimeout time.Duration
	maxHistory     int
}

// parseAndValidateHelmReleaseFlags parses the five HelmRelease generation
// flags and rejects malformed or non-positive durations and negative
// MaxHistory. Same shape as cozystack-operator's main.
func (o *CozyServerOptions) parseAndValidateHelmReleaseFlags() (helmReleaseFlagValues, error) {
	var v helmReleaseFlagValues
	var err error
	if v.interval, err = config.ParsePositiveDuration("--helmrelease-interval", o.HelmReleaseInterval); err != nil {
		return v, err
	}
	if v.retryInterval, err = config.ParsePositiveDuration("--helmrelease-retry-interval", o.HelmReleaseRetryInterval); err != nil {
		return v, err
	}
	if v.installTimeout, err = config.ParsePositiveDuration("--helmrelease-install-timeout", o.HelmReleaseInstallTimeout); err != nil {
		return v, err
	}
	if v.upgradeTimeout, err = config.ParsePositiveDuration("--helmrelease-upgrade-timeout", o.HelmReleaseUpgradeTimeout); err != nil {
		return v, err
	}
	if o.HelmReleaseMaxHistory < 0 {
		return v, fmt.Errorf("--helmrelease-max-history must be >= 0 (got %d)", o.HelmReleaseMaxHistory)
	}
	v.maxHistory = o.HelmReleaseMaxHistory
	return v, nil
}

// Complete fills in the fields that are not set
func (o *CozyServerOptions) Complete() error {
	hrFlags, err := o.parseAndValidateHelmReleaseFlags()
	if err != nil {
		return err
	}

	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("failed to register types: %w", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("failed to register core types: %w", err)
	}

	cfg, err := k8sconfig.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	o.Client, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("client initialization failed: %w", err)
	}

	crdList := &v1alpha1.ApplicationDefinitionList{}

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
			return fmt.Errorf("failed to list ApplicationDefinitions after %v: %w", maxRetryDuration, err)
		}

		// Log the error and wait before retrying
		fmt.Printf("Failed to list ApplicationDefinitions (retrying in %v): %v\n", delay, err)
		time.Sleep(delay)

		delay = time.Duration(float64(delay) * 1.5)
		if delay > maxDelay {
			delay = maxDelay
		}
	}

	// Convert to ResourceConfig
	o.ResourceConfig = &config.ResourceConfig{}
	for _, crd := range crdList.Items {
		release := config.ReleaseConfig{
			Prefix: crd.Spec.Release.Prefix,
			Labels: crd.Spec.Release.Labels,
			ChartRef: config.ChartRefConfig{
				Kind:      crd.Spec.Release.ChartRef.Kind,
				Name:      crd.Spec.Release.ChartRef.Name,
				Namespace: crd.Spec.Release.ChartRef.Namespace,
			},
			// Per-Application HelmRelease generation defaults from server
			// flags. The same five values are applied to every Resource,
			// matching cozystack-operator's PackageReconciler. The
			// per-Application HelmInstallTimeout annotation populated below
			// still wins over HelmReleaseInstallTimeout/UpgradeTimeout.
			HelmReleaseInterval:       hrFlags.interval,
			HelmReleaseRetryInterval:  hrFlags.retryInterval,
			HelmReleaseInstallTimeout: hrFlags.installTimeout,
			HelmReleaseUpgradeTimeout: hrFlags.upgradeTimeout,
			HelmReleaseMaxHistory:     hrFlags.maxHistory,
		}
		// Per-Application HelmRelease Install/Upgrade timeout. Applications
		// whose parent chart contains asynchronously-provisioned resources
		// the chart itself depends on (for example, the Kamaji-provisioned
		// admin-kubeconfig Secret for Kubernetes tenants) need a longer
		// wait budget than the Flux default. Consumed by the REST storage
		// layer when building the HelmRelease Spec. The parser rejects
		// units Flux would reject at webhook time, so a bad annotation
		// surfaces as a loud startup failure instead of a silent drop to
		// defaults. helm-install-timeout covers both install and upgrade;
		// helm-upgrade-timeout overrides only the upgrade side for kinds
		// that need an asymmetric budget.
		installTimeout, err := config.ParseHelmTimeoutAnnotation(
			crd.Annotations[config.HelmInstallTimeoutAnnotation],
		)
		if err != nil {
			return fmt.Errorf(
				"ApplicationDefinition %q has invalid %s annotation: %w",
				crd.Name, config.HelmInstallTimeoutAnnotation, err,
			)
		}
		release.HelmInstallTimeout = installTimeout
		upgradeTimeout, err := config.ParseHelmTimeoutAnnotation(
			crd.Annotations[config.HelmUpgradeTimeoutAnnotation],
		)
		if err != nil {
			return fmt.Errorf(
				"ApplicationDefinition %q has invalid %s annotation: %w",
				crd.Name, config.HelmUpgradeTimeoutAnnotation, err,
			)
		}
		release.HelmUpgradeTimeout = upgradeTimeout
		disableWait, err := config.ParseHelmInstallDisableWaitAnnotation(
			crd.Annotations[config.HelmInstallDisableWaitAnnotation],
		)
		if err != nil {
			return fmt.Errorf(
				"ApplicationDefinition %q has invalid %s annotation: %w",
				crd.Name, config.HelmInstallDisableWaitAnnotation, err,
			)
		}
		release.HelmInstallDisableWait = disableWait
		resource := config.Resource{
			Application: config.ApplicationConfig{
				Kind:          crd.Spec.Application.Kind,
				Singular:      crd.Spec.Application.Singular,
				Plural:        crd.Spec.Application.Plural,
				ShortNames:    []string{}, // TODO: implement shortnames
				OpenAPISchema: crd.Spec.Application.OpenAPISchema,
			},
			Release: release,
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

	apiVersion := "0.1"
	if o.ResourceConfig != nil {
		raw, err := json.Marshal(o.ResourceConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal resource config: %v", err)
		}
		sum := sha256.Sum256(raw)
		apiVersion = "0.1-" + hex.EncodeToString(sum[:8])
	}

	kindSchemas := KindSchemasFromConfig(o.ResourceConfig)
	ConfigureOpenAPI(&serverConfig.Config, kindSchemas, "Cozy", apiVersion)

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
