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
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

var addCmdFlags struct {
	files      []string
	kubeconfig string
}

var addCmd = &cobra.Command{
	Use:   "add [package]...",
	Short: "Install PackageSource and its dependencies interactively",
	Long: `Install PackageSource and its dependencies interactively.

You can specify packages as arguments or use -f flag to read from files.
Multiple -f flags can be specified, and they can point to files or directories.`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		// Collect package names from arguments and files
		packageNames := make(map[string]bool)
		packagesFromFiles := make(map[string]string) // packageName -> filePath
		
		for _, arg := range args {
			packageNames[arg] = true
		}

		// Read packages from files
		for _, filePath := range addCmdFlags.files {
			packages, err := readPackagesFromFile(filePath)
			if err != nil {
				return fmt.Errorf("failed to read packages from %s: %w", filePath, err)
			}
			for _, pkg := range packages {
				packageNames[pkg] = true
				if oldPath, ok := packagesFromFiles[pkg]; ok {
					fmt.Fprintf(os.Stderr, "warning: package %q is defined in both %s and %s, using the latter\n", pkg, oldPath, filePath)
				}
				packagesFromFiles[pkg] = filePath
			}
		}

		if len(packageNames) == 0 {
			return fmt.Errorf("no packages specified")
		}

		// Create Kubernetes client config
		var config *rest.Config
		var err error

		if addCmdFlags.kubeconfig != "" {
			config, err = clientcmd.BuildConfigFromFlags("", addCmdFlags.kubeconfig)
			if err != nil {
				return fmt.Errorf("failed to load kubeconfig from %s: %w", addCmdFlags.kubeconfig, err)
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

		// Process each package
		for packageName := range packageNames {
			// Check if package comes from a file
			if filePath, fromFile := packagesFromFiles[packageName]; fromFile {
				// Try to create Package directly from file
				if err := createPackageFromFile(ctx, k8sClient, filePath, packageName); err == nil {
					fmt.Fprintf(os.Stderr, "✓ Added Package %s\n", packageName)
					continue
				}
				// If failed, fall back to interactive installation
			}
			
			// Interactive installation from PackageSource
			if err := installPackage(ctx, k8sClient, packageName); err != nil {
				return err
			}
		}

		return nil
	},
}

func readPackagesFromFile(filePath string) ([]string, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return nil, err
	}

	var packages []string

	if info.IsDir() {
		// Read all YAML files from directory
		err := filepath.Walk(filePath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
				return nil
			}

			pkgs, err := readPackagesFromYAMLFile(path)
			if err != nil {
				return fmt.Errorf("failed to read %s: %w", path, err)
			}
			packages = append(packages, pkgs...)
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		packages, err = readPackagesFromYAMLFile(filePath)
		if err != nil {
			return nil, err
		}
	}

	return packages, nil
}

func readPackagesFromYAMLFile(filePath string) ([]string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var packages []string

	// Split YAML documents (in case of multiple resources)
	documents := strings.Split(string(data), "---")
	
	for _, doc := range documents {
		doc = strings.TrimSpace(doc)
		if doc == "" {
			continue
		}

		// Parse using Kubernetes decoder
		decoder := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
		obj := &unstructured.Unstructured{}
		_, _, err := decoder.Decode([]byte(doc), nil, obj)
		if err != nil {
			continue
		}

		// Check if it's a Package
		if obj.GetKind() == "Package" {
			name := obj.GetName()
			if name != "" {
				packages = append(packages, name)
			}
			continue
		}

		// Check if it's a PackageSource
		if obj.GetKind() == "PackageSource" {
			name := obj.GetName()
			if name != "" {
				packages = append(packages, name)
			}
			continue
		}

		// Try to parse as PackageList or PackageSourceList
		if obj.GetKind() == "PackageList" || obj.GetKind() == "PackageSourceList" {
			items, found, err := unstructured.NestedSlice(obj.Object, "items")
			if err == nil && found {
				for _, item := range items {
					if itemMap, ok := item.(map[string]interface{}); ok {
						if metadata, ok := itemMap["metadata"].(map[string]interface{}); ok {
							if name, ok := metadata["name"].(string); ok && name != "" {
								packages = append(packages, name)
							}
						}
					}
				}
			}
			continue
		}
	}

	// Return empty list if no packages found - don't error out
	// The check for whether any packages were specified at all is handled later in RunE
	return packages, nil
}

// buildDependencyTree builds a dependency tree starting from the root PackageSource
// Returns both the dependency tree and a map of dependencies to their requesters
func buildDependencyTree(ctx context.Context, k8sClient client.Client, rootName string) (map[string][]string, map[string]string, error) {
	tree := make(map[string][]string)
	dependencyRequesters := make(map[string]string) // dep -> requester
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
			// Track who requested this dependency
			dependencyRequesters[dep] = pkgName
			// Recursively build tree for dependencies
			if err := buildTree(dep); err != nil {
				return err
			}
		}

		return nil
	}

	if err := buildTree(rootName); err != nil {
		return nil, nil, err
	}

	return tree, dependencyRequesters, nil
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

// createPackageFromFile creates a Package resource directly from a YAML file
func createPackageFromFile(ctx context.Context, k8sClient client.Client, filePath string, packageName string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}

	// Split YAML documents
	documents := strings.Split(string(data), "---")
	
	for _, doc := range documents {
		doc = strings.TrimSpace(doc)
		if doc == "" {
			continue
		}

		// Parse using Kubernetes decoder
		decoder := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
		obj := &unstructured.Unstructured{}
		_, _, err := decoder.Decode([]byte(doc), nil, obj)
		if err != nil {
			continue
		}

		// Check if it's a Package with matching name
		if obj.GetKind() == "Package" && obj.GetName() == packageName {
			// Convert to Package
			var pkg cozyv1alpha1.Package
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &pkg); err != nil {
				return fmt.Errorf("failed to convert Package: %w", err)
			}

			// Create Package
			if err := k8sClient.Create(ctx, &pkg); err != nil {
				return fmt.Errorf("failed to create Package: %w", err)
			}

			return nil
		}
	}

	return fmt.Errorf("Package %s not found in file", packageName)
}

func installPackage(ctx context.Context, k8sClient client.Client, packageSourceName string) error {
	// Get PackageSource
	packageSource := &cozyv1alpha1.PackageSource{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: packageSourceName}, packageSource); err != nil {
		return fmt.Errorf("failed to get PackageSource %s: %w", packageSourceName, err)
	}

	// Build dependency tree
	dependencyTree, dependencyRequesters, err := buildDependencyTree(ctx, k8sClient, packageSourceName)
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

	// First, collect all variant selections
	fmt.Fprintf(os.Stderr, "Installing %s and its dependencies...\n\n", packageSourceName)
	packageVariants := make(map[string]string) // packageName -> variant

	for _, pkgName := range installOrder {
		// Check if already installed
		if installed, exists := installedMap[pkgName]; exists {
			variant := installed.Spec.Variant
			if variant == "" {
				variant = "default"
			}
			fmt.Fprintf(os.Stderr, "✓ %s (already installed, variant: %s)\n", pkgName, variant)
			packageVariants[pkgName] = variant
			continue
		}

		// Get PackageSource for this dependency
		ps, exists := packageSourceMap[pkgName]
		if !exists {
			requester := dependencyRequesters[pkgName]
			if requester != "" {
				return fmt.Errorf("PackageSource %s not found (required by %s)", pkgName, requester)
			}
			return fmt.Errorf("PackageSource %s not found", pkgName)
		}

		// Select variant interactively
		variant, err := selectVariantInteractive(ps)
		if err != nil {
			return fmt.Errorf("failed to select variant for %s: %w", pkgName, err)
		}

		packageVariants[pkgName] = variant
	}

	// Now create all Package resources
	for _, pkgName := range installOrder {
		// Skip if already installed
		if _, exists := installedMap[pkgName]; exists {
			continue
		}

		variant := packageVariants[pkgName]

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

		fmt.Fprintf(os.Stderr, "✓ Added Package %s\n", pkgName)
	}

	return nil
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

	// If only one variant, use it as default
	defaultVariant := ps.Spec.Variants[0].Name
	var prompt string
	if len(ps.Spec.Variants) == 1 {
		prompt = "Select variant [1]: "
	} else {
		prompt = fmt.Sprintf("Select variant (1-%d): ", len(ps.Spec.Variants))
	}

	for {
		fmt.Fprintf(os.Stderr, prompt)
		input, err := reader.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("failed to read input: %w", err)
		}

		input = strings.TrimSpace(input)

		// If input is empty and there's a default variant, use it
		if input == "" && len(ps.Spec.Variants) == 1 {
			return defaultVariant, nil
		}

		choice, err := strconv.Atoi(input)
		if err != nil || choice < 1 || choice > len(ps.Spec.Variants) {
			fmt.Fprintf(os.Stderr, "Invalid choice. Please enter a number between 1 and %d.\n", len(ps.Spec.Variants))
			continue
		}

		return ps.Spec.Variants[choice-1].Name, nil
	}
}

func init() {
	rootCmd.AddCommand(addCmd)
	addCmd.Flags().StringArrayVarP(&addCmdFlags.files, "file", "f", []string{}, "Read packages from file or directory (can be specified multiple times)")
	addCmd.Flags().StringVar(&addCmdFlags.kubeconfig, "kubeconfig", "", "Path to kubeconfig file (defaults to ~/.kube/config or KUBECONFIG env var)")
}

