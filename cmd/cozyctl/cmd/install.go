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
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

var installCmdFlags struct {
	kubeconfig string
}

var installCmd = &cobra.Command{
	Use:   "install PACKAGESOURCE",
	Short: "Install a PackageSource and its dependencies interactively",
	Long: `Install a PackageSource and its dependencies interactively.

This command builds a dependency tree for the specified PackageSource and
prompts you to install all dependencies, starting from the root of the tree.
For each dependency, you'll be asked to choose a variant, even if only one is available.

If a dependency is already installed, the variant selection will be skipped.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		packageSourceName := args[0]

		// Create Kubernetes client config
		var config *rest.Config
		var err error

		if installCmdFlags.kubeconfig != "" {
			config, err = clientcmd.BuildConfigFromFlags("", installCmdFlags.kubeconfig)
			if err != nil {
				return fmt.Errorf("failed to load kubeconfig from %s: %w", installCmdFlags.kubeconfig, err)
			}
		} else {
			config, err = ctrl.GetConfig()
			if err != nil {
				return fmt.Errorf("failed to get kubeconfig: %w", err)
			}
		}

		scheme := runtime.NewScheme()
		utilruntime.Must(clientgoscheme.AddToScheme(scheme))
		utilruntime.Must(cozyv1alpha1.AddToScheme(scheme))

		k8sClient, err := client.New(config, client.Options{Scheme: scheme})
		if err != nil {
			return fmt.Errorf("failed to create k8s client: %w", err)
		}

		// Get PackageSource
		packageSource := &cozyv1alpha1.PackageSource{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: packageSourceName}, packageSource); err != nil {
			return fmt.Errorf("failed to get PackageSource %s: %w", packageSourceName, err)
		}

		// Build dependency tree
		dependencyTree, err := buildDependencyTree(ctx, k8sClient, packageSourceName)
		if err != nil {
			return fmt.Errorf("failed to build dependency tree: %w", err)
		}

		// Topological sort (install from root to leaves)
		installOrder, err := topologicalSort(dependencyTree)
		if err != nil {
			return fmt.Errorf("failed to sort dependencies: %w", err)
		}

		// Get all PackageSources for variant selection
		var allPackageSources cozyv1alpha1.PackageSourceList
		if err := k8sClient.List(ctx, &allPackageSources); err != nil {
			return fmt.Errorf("failed to list PackageSources: %w", err)
		}

		packageSourceMap := make(map[string]*cozyv1alpha1.PackageSource)
		for i := range allPackageSources.Items {
			packageSourceMap[allPackageSources.Items[i].Name] = &allPackageSources.Items[i]
		}

		// Get all installed Packages
		var installedPackages cozyv1alpha1.PackageList
		if err := k8sClient.List(ctx, &installedPackages); err != nil {
			return fmt.Errorf("failed to list Packages: %w", err)
		}

		installedMap := make(map[string]*cozyv1alpha1.Package)
		for i := range installedPackages.Items {
			installedMap[installedPackages.Items[i].Name] = &installedPackages.Items[i]
		}

		// Install dependencies in order
		fmt.Fprintf(os.Stderr, "Installing %s and its dependencies...\n\n", packageSourceName)
		for _, pkgName := range installOrder {
			// Check if already installed
			if installed, exists := installedMap[pkgName]; exists {
				variant := installed.Spec.Variant
				if variant == "" {
					variant = "default"
				}
				fmt.Fprintf(os.Stderr, "✓ %s (already installed, variant: %s)\n", pkgName, variant)
				continue
			}

			// Get PackageSource for this dependency
			ps, exists := packageSourceMap[pkgName]
			if !exists {
				fmt.Fprintf(os.Stderr, "⚠ Warning: PackageSource %s not found, skipping\n", pkgName)
				continue
			}

			// Select variant interactively
			variant, err := selectVariantInteractive(ps)
			if err != nil {
				return fmt.Errorf("failed to select variant for %s: %w", pkgName, err)
			}

			// Create Package
			pkg := &cozyv1alpha1.Package{
				ObjectMeta: metav1.ObjectMeta{
					Name: pkgName,
				},
				Spec: cozyv1alpha1.PackageSpec{
					Variant: variant,
				},
			}

			if err := k8sClient.Create(ctx, pkg); err != nil {
				return fmt.Errorf("failed to create Package %s: %w", pkgName, err)
			}

			fmt.Fprintf(os.Stderr, "✓ Created Package %s with variant %s\n", pkgName, variant)
		}

		fmt.Fprintf(os.Stderr, "\nInstallation complete!\n")
		return nil
	},
}

// buildDependencyTree builds a dependency tree starting from the root PackageSource
func buildDependencyTree(ctx context.Context, k8sClient client.Client, rootName string) (map[string][]string, error) {
	tree := make(map[string][]string)
	visited := make(map[string]bool)
	
	// Ensure root is in tree even if it has no dependencies
	tree[rootName] = []string{}

	var buildTree func(string) error
	buildTree = func(pkgName string) error {
		if visited[pkgName] {
			return nil
		}
		visited[pkgName] = true

		// Get PackageSource
		ps := &cozyv1alpha1.PackageSource{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: pkgName}, ps); err != nil {
			// If PackageSource doesn't exist, just skip it
			return nil
		}

		// Collect all dependencies from all variants
		deps := make(map[string]bool)
		for _, variant := range ps.Spec.Variants {
			for _, dep := range variant.DependsOn {
				deps[dep] = true
			}
		}

		// Add dependencies to tree
		for dep := range deps {
			if _, exists := tree[pkgName]; !exists {
				tree[pkgName] = []string{}
			}
			tree[pkgName] = append(tree[pkgName], dep)
			// Recursively build tree for dependencies
			if err := buildTree(dep); err != nil {
				return err
			}
		}

		return nil
	}

	if err := buildTree(rootName); err != nil {
		return nil, err
	}

	return tree, nil
}

// topologicalSort performs topological sort on the dependency tree
// Returns order from root to leaves (dependencies first)
func topologicalSort(tree map[string][]string) ([]string, error) {
	// Build reverse graph (dependencies -> dependents)
	reverseGraph := make(map[string][]string)
	allNodes := make(map[string]bool)

	for node, deps := range tree {
		allNodes[node] = true
		for _, dep := range deps {
			allNodes[dep] = true
			reverseGraph[dep] = append(reverseGraph[dep], node)
		}
	}

	// Calculate in-degrees (how many dependencies a node has)
	inDegree := make(map[string]int)
	for node := range allNodes {
		inDegree[node] = 0
	}
	for node, deps := range tree {
		inDegree[node] = len(deps)
	}

	// Kahn's algorithm - start with nodes that have no dependencies
	var queue []string
	for node, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, node)
		}
	}

	var result []string
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		result = append(result, node)

		// Process dependents (nodes that depend on this node)
		for _, dependent := range reverseGraph[node] {
			inDegree[dependent]--
			if inDegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}

	// Check for cycles
	if len(result) != len(allNodes) {
		return nil, fmt.Errorf("dependency cycle detected")
	}

	return result, nil
}

// selectVariantInteractive prompts user to select a variant
func selectVariantInteractive(ps *cozyv1alpha1.PackageSource) (string, error) {
	if len(ps.Spec.Variants) == 0 {
		return "", fmt.Errorf("no variants available for PackageSource %s", ps.Name)
	}

	reader := bufio.NewReader(os.Stdin)

	fmt.Fprintf(os.Stderr, "\nPackageSource: %s\n", ps.Name)
	fmt.Fprintf(os.Stderr, "Available variants:\n")
	for i, variant := range ps.Spec.Variants {
		fmt.Fprintf(os.Stderr, "  %d. %s\n", i+1, variant.Name)
	}

	for {
		fmt.Fprintf(os.Stderr, "Select variant (1-%d): ", len(ps.Spec.Variants))
		input, err := reader.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("failed to read input: %w", err)
		}

		input = strings.TrimSpace(input)
		choice, err := strconv.Atoi(input)
		if err != nil || choice < 1 || choice > len(ps.Spec.Variants) {
			fmt.Fprintf(os.Stderr, "Invalid choice. Please enter a number between 1 and %d.\n", len(ps.Spec.Variants))
			continue
		}

		return ps.Spec.Variants[choice-1].Name, nil
	}
}

func init() {
	rootCmd.AddCommand(installCmd)
	installCmd.Flags().StringVar(&installCmdFlags.kubeconfig, "kubeconfig", "", "Path to kubeconfig file (defaults to ~/.kube/config or KUBECONFIG env var)")
}

