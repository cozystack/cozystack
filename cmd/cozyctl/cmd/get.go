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
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	corev1alpha1 "github.com/cozystack/cozystack/pkg/apis/core/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var getCmdFlags struct {
	target string
}

var getCmd = &cobra.Command{
	Use:   "get <type> [name]",
	Short: "Display one or many resources",
	Long: `Display one or many resources.

Built-in types:
  ns, namespaces      Tenant namespaces (cluster-scoped)
  modules             Tenant modules
  pvc, pvcs           PersistentVolumeClaims

Sub-resource types (use -t to filter by parent application):
  secrets             Secrets
  services, svc       Services
  ingresses, ing      Ingresses
  workloads           WorkloadMonitors

Application types are discovered dynamically from ApplicationDefinitions.
Use -t type/name to filter sub-resources by a specific application.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runGet,
}

func init() {
	rootCmd.AddCommand(getCmd)
	getCmd.Flags().StringVarP(&getCmdFlags.target, "target", "t", "", "Filter sub-resources by application type/name")
}

func runGet(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	resourceType := args[0]
	var resourceName string
	if len(args) > 1 {
		resourceName = args[1]
	}

	switch strings.ToLower(resourceType) {
	case "ns", "namespace", "namespaces":
		return getNamespaces(ctx, resourceName)
	case "module", "modules":
		return getModules(ctx, resourceName)
	case "pvc", "pvcs", "persistentvolumeclaim", "persistentvolumeclaims":
		return getPVCs(ctx, resourceName)
	case "secret", "secrets":
		return getSubResources(ctx, "secrets", resourceName)
	case "service", "services", "svc":
		return getSubResources(ctx, "services", resourceName)
	case "ingress", "ingresses", "ing":
		return getSubResources(ctx, "ingresses", resourceName)
	case "workload", "workloads":
		return getSubResources(ctx, "workloads", resourceName)
	default:
		return getApplications(ctx, resourceType, resourceName)
	}
}

func getNamespaces(ctx context.Context, name string) error {
	_, dynClient, err := newClients()
	if err != nil {
		return err
	}

	gvr := schema.GroupVersionResource{Group: "core.cozystack.io", Version: "v1alpha1", Resource: "tenantnamespaces"}

	if name != "" {
		item, err := dynClient.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get namespace %q: %w", name, err)
		}
		printNamespaces([]unstructured.Unstructured{*item})
		return nil
	}

	list, err := dynClient.Resource(gvr).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list namespaces: %w", err)
	}
	if len(list.Items) == 0 {
		printNoResources(os.Stderr, "namespaces")
		return nil
	}
	printNamespaces(list.Items)
	return nil
}

func getModules(ctx context.Context, name string) error {
	_, dynClient, err := newClients()
	if err != nil {
		return err
	}

	ns, err := getNamespace()
	if err != nil {
		return err
	}

	gvr := schema.GroupVersionResource{Group: "core.cozystack.io", Version: "v1alpha1", Resource: "tenantmodules"}

	if name != "" {
		item, err := dynClient.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get module %q: %w", name, err)
		}
		printModules([]unstructured.Unstructured{*item})
		return nil
	}

	list, err := dynClient.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list modules: %w", err)
	}
	if len(list.Items) == 0 {
		printNoResources(os.Stderr, "modules")
		return nil
	}
	printModules(list.Items)
	return nil
}

func getPVCs(ctx context.Context, name string) error {
	_, dynClient, err := newClients()
	if err != nil {
		return err
	}

	ns, err := getNamespace()
	if err != nil {
		return err
	}

	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}

	if name != "" {
		item, err := dynClient.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get PVC %q: %w", name, err)
		}
		printPVCs([]unstructured.Unstructured{*item})
		return nil
	}

	list, err := dynClient.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list PVCs: %w", err)
	}
	if len(list.Items) == 0 {
		printNoResources(os.Stderr, "PVCs")
		return nil
	}
	printPVCs(list.Items)
	return nil
}

func getSubResources(ctx context.Context, subType string, name string) error {
	typedClient, dynClient, err := newClients()
	if err != nil {
		return err
	}

	ns, err := getNamespace()
	if err != nil {
		return err
	}

	labelSelector, err := buildSubResourceSelector(ctx, typedClient, getCmdFlags.target)
	if err != nil {
		return err
	}

	switch subType {
	case "secrets":
		return getFilteredSecrets(ctx, dynClient, ns, name, labelSelector)
	case "services":
		return getFilteredServices(ctx, dynClient, ns, name, labelSelector)
	case "ingresses":
		return getFilteredIngresses(ctx, dynClient, ns, name, labelSelector)
	case "workloads":
		return getFilteredWorkloads(ctx, dynClient, ns, name, labelSelector)
	default:
		return fmt.Errorf("unknown sub-resource type: %s", subType)
	}
}

func buildSubResourceSelector(ctx context.Context, typedClient client.Client, target string) (string, error) {
	var selectors []string

	if target == "" {
		selectors = append(selectors, corev1alpha1.TenantResourceLabelKey+"="+corev1alpha1.TenantResourceLabelValue)
		return strings.Join(selectors, ","), nil
	}

	parts := strings.SplitN(target, "/", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid target format %q, expected type/name", target)
	}
	targetType, targetName := parts[0], parts[1]

	// Discover ApplicationDefinitions to resolve the target type
	registry, err := discoverAppDefs(ctx, typedClient)
	if err != nil {
		return "", err
	}

	// Check if this is a module reference
	if strings.ToLower(targetType) == "module" {
		info := registry.ResolveModule(targetName)
		if info == nil {
			return "", fmt.Errorf("unknown module %q", targetName)
		}
		selectors = append(selectors,
			appsv1alpha1.ApplicationKindLabel+"="+info.Kind,
			appsv1alpha1.ApplicationNameLabel+"="+targetName,
			corev1alpha1.TenantResourceLabelKey+"="+corev1alpha1.TenantResourceLabelValue,
		)
		return strings.Join(selectors, ","), nil
	}

	info := registry.Resolve(targetType)
	if info == nil {
		return "", fmt.Errorf("unknown application type %q", targetType)
	}

	selectors = append(selectors,
		appsv1alpha1.ApplicationKindLabel+"="+info.Kind,
		appsv1alpha1.ApplicationNameLabel+"="+targetName,
		corev1alpha1.TenantResourceLabelKey+"="+corev1alpha1.TenantResourceLabelValue,
	)
	return strings.Join(selectors, ","), nil
}

func getFilteredSecrets(ctx context.Context, dynClient dynamic.Interface, ns, name, labelSelector string) error {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
	return getFilteredResources(ctx, dynClient, gvr, ns, name, labelSelector, "secrets", printSecrets)
}

func getFilteredServices(ctx context.Context, dynClient dynamic.Interface, ns, name, labelSelector string) error {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}
	return getFilteredResources(ctx, dynClient, gvr, ns, name, labelSelector, "services", printServices)
}

func getFilteredIngresses(ctx context.Context, dynClient dynamic.Interface, ns, name, labelSelector string) error {
	gvr := schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}
	return getFilteredResources(ctx, dynClient, gvr, ns, name, labelSelector, "ingresses", printIngresses)
}

func getFilteredWorkloads(ctx context.Context, dynClient dynamic.Interface, ns, name, labelSelector string) error {
	gvr := schema.GroupVersionResource{Group: "cozystack.io", Version: "v1alpha1", Resource: "workloadmonitors"}
	return getFilteredResources(ctx, dynClient, gvr, ns, name, labelSelector, "workloads", printWorkloads)
}

func getFilteredResources(
	ctx context.Context,
	dynClient dynamic.Interface,
	gvr schema.GroupVersionResource,
	ns, name, labelSelector string,
	typeName string,
	printer func([]unstructured.Unstructured),
) error {
	if name != "" {
		item, err := dynClient.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get %s %q: %w", typeName, name, err)
		}
		printer([]unstructured.Unstructured{*item})
		return nil
	}

	list, err := dynClient.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list %s: %w", typeName, err)
	}
	if len(list.Items) == 0 {
		printNoResources(os.Stderr, typeName)
		return nil
	}
	printer(list.Items)
	return nil
}

func getApplications(ctx context.Context, resourceType, name string) error {
	typedClient, dynClient, err := newClients()
	if err != nil {
		return err
	}

	ns, err := getNamespace()
	if err != nil {
		return err
	}

	registry, err := discoverAppDefs(ctx, typedClient)
	if err != nil {
		return err
	}

	info := registry.Resolve(resourceType)
	if info == nil {
		return fmt.Errorf("unknown resource type %q\nUse 'cozyctl get --help' for available types", resourceType)
	}

	gvr := schema.GroupVersionResource{Group: "apps.cozystack.io", Version: "v1alpha1", Resource: info.Plural}

	if name != "" {
		item, err := dynClient.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get %s %q: %w", info.Singular, name, err)
		}
		printApplications([]unstructured.Unstructured{*item})
		return nil
	}

	list, err := dynClient.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list %s: %w", info.Plural, err)
	}
	if len(list.Items) == 0 {
		printNoResources(os.Stderr, info.Plural)
		return nil
	}
	printApplications(list.Items)
	return nil
}
