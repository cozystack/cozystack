/*
Copyright 2025 The Cozystack Authors.

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

package cmd

import (
	"fmt"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func buildRestConfig() (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if globalFlags.kubeconfig != "" {
		rules.ExplicitPath = globalFlags.kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	if globalFlags.context != "" {
		overrides.CurrentContext = globalFlags.context
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}
	return config, nil
}

func newScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(cozyv1alpha1.AddToScheme(scheme))
	return scheme
}

func newClients() (client.Client, dynamic.Interface, error) {
	config, err := buildRestConfig()
	if err != nil {
		return nil, nil, err
	}

	scheme := newScheme()

	typedClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create k8s client: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return typedClient, dynClient, nil
}

func getNamespace() (string, error) {
	if globalFlags.namespace != "" {
		return globalFlags.namespace, nil
	}

	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if globalFlags.kubeconfig != "" {
		rules.ExplicitPath = globalFlags.kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	if globalFlags.context != "" {
		overrides.CurrentContext = globalFlags.context
	}

	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
	ns, _, err := clientConfig.Namespace()
	if err != nil {
		return "", fmt.Errorf("failed to determine namespace: %w", err)
	}
	if ns == "" {
		ns = "default"
	}
	return ns, nil
}

// getRestConfig is a convenience function when only the rest.Config is needed
// (used by buildRestConfig but also available for other callers).
func getRestConfig() (*rest.Config, error) {
	if globalFlags.kubeconfig != "" || globalFlags.context != "" {
		return buildRestConfig()
	}
	config, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig: %w", err)
	}
	return config, nil
}
