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
	"strings"

	"github.com/spf13/cobra"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

var delCmdFlags struct {
	files      []string
	kubeconfig string
}

var delCmd = &cobra.Command{
	Use:   "del [package]...",
	Short: "Delete Package resources",
	Long: `Delete Package resources.

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

		// Read packages from files (reuse function from add.go)
		for _, filePath := range delCmdFlags.files {
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

		if delCmdFlags.kubeconfig != "" {
			config, err = clientcmd.BuildConfigFromFlags("", delCmdFlags.kubeconfig)
			if err != nil {
				return fmt.Errorf("failed to load kubeconfig from %s: %w", delCmdFlags.kubeconfig, err)
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

		// Check which requested packages are installed
		var installedPackages cozyv1alpha1.PackageList
		if err := k8sClient.List(ctx, &installedPackages); err != nil {
			return fmt.Errorf("failed to list Packages: %w", err)
		}
		installedMap := make(map[string]bool)
		for _, pkg := range installedPackages.Items {
			installedMap[pkg.Name] = true
		}

		// Warn about requested packages that are not installed
		for pkgName := range packageNames {
			if !installedMap[pkgName] {
				fmt.Fprintf(os.Stderr, "⚠ Package %s is not installed, skipping\n", pkgName)
			}
		}

		// Find all packages to delete (including dependents)
		packagesToDelete, err := findPackagesToDelete(ctx, k8sClient, packageNames)
		if err != nil {
			return fmt.Errorf("failed to analyze dependencies: %w", err)
		}

		if len(packagesToDelete) == 0 {
			fmt.Fprintf(os.Stderr, "No packages found to delete\n")
			return nil
		}

		// Show packages to be deleted and ask for confirmation
		if err := confirmDeletion(packagesToDelete, packageNames); err != nil {
			return err
		}

		// Delete packages in reverse topological order (dependents first, then dependencies)
		deleteOrder, err := getDeleteOrder(ctx, k8sClient, packagesToDelete)
		if err != nil {
			return fmt.Errorf("failed to determine delete order: %w", err)
		}

		// Delete each package
		for _, packageName := range deleteOrder {
			pkg := &cozyv1alpha1.Package{}
			pkg.Name = packageName
			if err := k8sClient.Delete(ctx, pkg); err != nil {
				if apierrors.IsNotFound(err) {
					fmt.Fprintf(os.Stderr, "⚠ Package %s not found, skipping\n", packageName)
					continue
				}
				return fmt.Errorf("failed to delete Package %s: %w", packageName, err)
			}
			fmt.Fprintf(os.Stderr, "✓ Deleted Package %s\n", packageName)
		}

		return nil
	},
}

// findPackagesToDelete finds all packages that need to be deleted, including dependents
func findPackagesToDelete(ctx context.Context, k8sClient client.Client, requestedPackages map[string]bool) (map[string]bool, error) {
	// Get all installed Packages
	var installedPackages cozyv1alpha1.PackageList
	if err := k8sClient.List(ctx, &installedPackages); err != nil {
		return nil, fmt.Errorf("failed to list Packages: %w", err)
	}

	installedMap := make(map[string]bool)
	for _, pkg := range installedPackages.Items {
		installedMap[pkg.Name] = true
	}

	// Get all PackageSources to build dependency graph
	var packageSources cozyv1alpha1.PackageSourceList
	if err := k8sClient.List(ctx, &packageSources); err != nil {
		return nil, fmt.Errorf("failed to list PackageSources: %w", err)
	}

	// Build reverse dependency graph (dependents -> dependencies)
	// This tells us: for each package, which packages depend on it
	reverseDeps := make(map[string][]string)
	for _, ps := range packageSources.Items {
		// Only consider installed packages
		if !installedMap[ps.Name] {
			continue
		}
		for _, variant := range ps.Spec.Variants {
			for _, dep := range variant.DependsOn {
				// Only consider installed dependencies
				if installedMap[dep] {
					reverseDeps[dep] = append(reverseDeps[dep], ps.Name)
				}
			}
		}
	}

	// Find all packages to delete (requested + their dependents)
	packagesToDelete := make(map[string]bool)
	visited := make(map[string]bool)

	var findDependents func(string)
	findDependents = func(pkgName string) {
		if visited[pkgName] {
			return
		}
		visited[pkgName] = true

		// Only add if it's installed
		if installedMap[pkgName] {
			packagesToDelete[pkgName] = true
		}

		// Recursively find all dependents
		for _, dependent := range reverseDeps[pkgName] {
			if installedMap[dependent] {
				findDependents(dependent)
			}
		}
	}

	// Start from requested packages
	for pkgName := range requestedPackages {
		if !installedMap[pkgName] {
			continue
		}
		findDependents(pkgName)
	}

	return packagesToDelete, nil
}

// confirmDeletion shows the list of packages to be deleted and asks for user confirmation
func confirmDeletion(packagesToDelete map[string]bool, requestedPackages map[string]bool) error {
	// Separate requested packages from dependents
	var requested []string
	var dependents []string

	for pkg := range packagesToDelete {
		if requestedPackages[pkg] {
			requested = append(requested, pkg)
		} else {
			dependents = append(dependents, pkg)
		}
	}

	fmt.Fprintf(os.Stderr, "\nThe following packages will be deleted:\n\n")

	if len(requested) > 0 {
		fmt.Fprintf(os.Stderr, "Requested packages:\n")
		for _, pkg := range requested {
			fmt.Fprintf(os.Stderr, "  - %s\n", pkg)
		}
		fmt.Fprintf(os.Stderr, "\n")
	}

	if len(dependents) > 0 {
		fmt.Fprintf(os.Stderr, "Dependent packages (will also be deleted):\n")
		for _, pkg := range dependents {
			fmt.Fprintf(os.Stderr, "  - %s\n", pkg)
		}
		fmt.Fprintf(os.Stderr, "\n")
	}

	fmt.Fprintf(os.Stderr, "Total: %d package(s)\n\n", len(packagesToDelete))
	fmt.Fprintf(os.Stderr, "Do you want to continue? [y/N]: ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read input: %w", err)
	}

	input = strings.TrimSpace(strings.ToLower(input))
	if input != "y" && input != "yes" {
		return fmt.Errorf("deletion cancelled")
	}

	return nil
}

// getDeleteOrder returns packages in reverse topological order (dependents first, then dependencies)
// This ensures we delete dependents before their dependencies
func getDeleteOrder(ctx context.Context, k8sClient client.Client, packagesToDelete map[string]bool) ([]string, error) {
	// Get all PackageSources to build dependency graph
	var packageSources cozyv1alpha1.PackageSourceList
	if err := k8sClient.List(ctx, &packageSources); err != nil {
		return nil, fmt.Errorf("failed to list PackageSources: %w", err)
	}

	// Build forward dependency graph (package -> dependencies)
	dependencyGraph := make(map[string][]string)
	for _, ps := range packageSources.Items {
		if !packagesToDelete[ps.Name] {
			continue
		}
		deps := make(map[string]bool)
		for _, variant := range ps.Spec.Variants {
			for _, dep := range variant.DependsOn {
				if packagesToDelete[dep] {
					deps[dep] = true
				}
			}
		}
		var depList []string
		for dep := range deps {
			depList = append(depList, dep)
		}
		dependencyGraph[ps.Name] = depList
	}

	// Build reverse graph for topological sort
	reverseGraph := make(map[string][]string)
	allNodes := make(map[string]bool)

	for node, deps := range dependencyGraph {
		allNodes[node] = true
		for _, dep := range deps {
			allNodes[dep] = true
			reverseGraph[dep] = append(reverseGraph[dep], node)
		}
	}

	// Add nodes that have no dependencies
	for pkg := range packagesToDelete {
		if !allNodes[pkg] {
			allNodes[pkg] = true
			dependencyGraph[pkg] = []string{}
		}
	}

	// Calculate in-degrees
	inDegree := make(map[string]int)
	for node := range allNodes {
		inDegree[node] = 0
	}
	for node, deps := range dependencyGraph {
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

		// Process dependents
		for _, dependent := range reverseGraph[node] {
			inDegree[dependent]--
			if inDegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}

	// Check for cycles: if not all nodes were processed, there's a cycle
	if len(result) != len(allNodes) {
		// Find unprocessed nodes
		processed := make(map[string]bool)
		for _, node := range result {
			processed[node] = true
		}
		var unprocessed []string
		for node := range allNodes {
			if !processed[node] {
				unprocessed = append(unprocessed, node)
			}
		}
		return nil, fmt.Errorf("dependency cycle detected: the following packages form a cycle and cannot be deleted: %v", unprocessed)
	}

	// Reverse the result to get dependents first, then dependencies
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	return result, nil
}

func init() {
	rootCmd.AddCommand(delCmd)
	delCmd.Flags().StringArrayVarP(&delCmdFlags.files, "file", "f", []string{}, "Read packages from file or directory (can be specified multiple times)")
	delCmd.Flags().StringVar(&delCmdFlags.kubeconfig, "kubeconfig", "", "Path to kubeconfig file (defaults to ~/.kube/config or KUBECONFIG env var)")
}

