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

	"github.com/emicklei/dot"
	"github.com/spf13/cobra"
	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

var dotCmdFlags struct {
	installed  bool
	components bool
	files      []string
	kubeconfig string
}

var dotCmd = &cobra.Command{
	Use:   "dot [package]...",
	Short: "Generate dependency graph as graphviz DOT format",
	Long: `Generate dependency graph as graphviz DOT format.

Pipe the output through the "dot" program (part of graphviz package) to render the graph:

    cozypkg dot | dot -Tpng > graph.png

By default, shows dependencies for all PackageSource resources.
Use --installed to show only installed Package resources.
Specify packages as arguments or use -f flag to read from files.`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		// Collect package names from arguments and files
		packageNames := make(map[string]bool)
		for _, arg := range args {
			packageNames[arg] = true
		}

		// Read packages from files (reuse function from add.go)
		for _, filePath := range dotCmdFlags.files {
			packages, err := readPackagesFromFile(filePath)
			if err != nil {
				return fmt.Errorf("failed to read packages from %s: %w", filePath, err)
			}
			for _, pkg := range packages {
				packageNames[pkg] = true
			}
		}

		// Convert to slice, empty means all packages
		var selectedPackages []string
		if len(packageNames) > 0 {
			for pkg := range packageNames {
				selectedPackages = append(selectedPackages, pkg)
			}
		}

		// If multiple packages specified, show graph for all of them
		// If single package, use packageName for backward compatibility
		var packageName string
		if len(selectedPackages) == 1 {
			packageName = selectedPackages[0]
		} else if len(selectedPackages) > 1 {
			// Multiple packages - pass empty string to packageName, use selectedPackages
			packageName = ""
		}

		// packagesOnly is inverse of components flag (if components=false, then packagesOnly=true)
		packagesOnly := !dotCmdFlags.components
		graph, allNodes, err := buildGraphFromCluster(ctx, dotCmdFlags.kubeconfig, packagesOnly, dotCmdFlags.installed, packageName, selectedPackages)
		if err != nil {
			return fmt.Errorf("error getting PackageSource dependencies: %w", err)
		}

		dotGraph := generateDOTGraph(graph, allNodes, packagesOnly)
		dotGraph.Write(os.Stdout)

		return nil
	},
}

func init() {
	rootCmd.AddCommand(dotCmd)
	dotCmd.Flags().BoolVarP(&dotCmdFlags.installed, "installed", "i", false, "show dependencies only for installed Package resources")
	dotCmd.Flags().BoolVar(&dotCmdFlags.components, "components", true, "show component-level dependencies (default: true)")
	dotCmd.Flags().StringArrayVarP(&dotCmdFlags.files, "file", "f", []string{}, "Read packages from file or directory (can be specified multiple times)")
	dotCmd.Flags().StringVar(&dotCmdFlags.kubeconfig, "kubeconfig", "", "Path to kubeconfig file (defaults to ~/.kube/config or KUBECONFIG env var)")
}

var (
	dependenciesScheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(dependenciesScheme))
	utilruntime.Must(cozyv1alpha1.AddToScheme(dependenciesScheme))
}

// buildGraphFromCluster builds a dependency graph from PackageSource resources in the cluster.
func buildGraphFromCluster(ctx context.Context, kubeconfig string, packagesOnly bool, installedOnly bool, packageName string, selectedPackages []string) (map[string][]string, map[string]bool, error) {
	// Create Kubernetes client config
	var config *rest.Config
	var err error

	if kubeconfig != "" {
		// Load kubeconfig from explicit path
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load kubeconfig from %s: %w", kubeconfig, err)
		}
	} else {
		// Use default kubeconfig loading (from env var or ~/.kube/config)
		config, err = ctrl.GetConfig()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get kubeconfig: %w", err)
		}
	}

	k8sClient, err := client.New(config, client.Options{Scheme: dependenciesScheme})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create k8s client: %w", err)
	}

	// Get installed Packages if needed
	installedPackages := make(map[string]bool)
	if installedOnly || packageName != "" {
		var packageList cozyv1alpha1.PackageList
		if err := k8sClient.List(ctx, &packageList); err != nil {
			return nil, nil, fmt.Errorf("failed to list Packages: %w", err)
		}
		for _, pkg := range packageList.Items {
			installedPackages[pkg.Name] = true
		}
	}

	// List all PackageSource resources
	var packageSourceList cozyv1alpha1.PackageSourceList
	if err := k8sClient.List(ctx, &packageSourceList); err != nil {
		return nil, nil, fmt.Errorf("failed to list PackageSources: %w", err)
	}

	graph := make(map[string][]string)
	allNodes := make(map[string]bool)

	// Process each PackageSource
	for _, ps := range packageSourceList.Items {
		psName := ps.Name
		if psName == "" {
			continue
		}

		// Filter by package name if specified
		if packageName != "" && psName != packageName {
			continue
		}

		// Filter by selected packages if specified
		if len(selectedPackages) > 0 {
			found := false
			for _, selected := range selectedPackages {
				if psName == selected {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Filter by installed packages if flag is set
		if installedOnly && !installedPackages[psName] {
			continue
		}

		allNodes[psName] = true

		// Extract dependencies from variants
		for _, variant := range ps.Spec.Variants {
			// Variant-level dependencies
			for _, dep := range variant.DependsOn {
				// If installedOnly is set, only include dependencies that are installed
				if installedOnly && !installedPackages[dep] {
					continue
				}
				graph[psName] = append(graph[psName], dep)
				allNodes[dep] = true
			}

			// Component-level dependencies
			if !packagesOnly {
				for _, component := range variant.Components {
					componentName := fmt.Sprintf("%s.%s", psName, component.Name)
					allNodes[componentName] = true

					if component.Install != nil {
						for _, dep := range component.Install.DependsOn {
							// Check if it's a local component dependency or external
							if strings.Contains(dep, ".") {
								graph[componentName] = append(graph[componentName], dep)
								allNodes[dep] = true
							} else {
								// Local component dependency
								localDep := fmt.Sprintf("%s.%s", psName, dep)
								graph[componentName] = append(graph[componentName], localDep)
								allNodes[localDep] = true
							}
						}
					}
				}
			}
		}
	}

	return graph, allNodes, nil
}

// generateDOTGraph generates a DOT graph from the dependency graph.
func generateDOTGraph(graph map[string][]string, allNodes map[string]bool, packagesOnly bool) *dot.Graph {
	g := dot.NewGraph(dot.Directed)
	g.Attr("rankdir", "LR")
	g.Attr("nodesep", "0.5")
	g.Attr("ranksep", "1.0")

	// Add nodes
	for node := range allNodes {
		if packagesOnly && strings.Contains(node, ".") && !strings.HasPrefix(node, "cozystack.") {
			// Skip component nodes when packages-only is enabled
			continue
		}

		n := g.Node(node)

		// Style nodes based on type
		if strings.Contains(node, ".") && !strings.HasPrefix(node, "cozystack.") {
			// Component node
			n.Attr("shape", "box")
			n.Attr("style", "rounded,filled")
			n.Attr("fillcolor", "lightyellow")
			n.Attr("label", strings.Split(node, ".")[len(strings.Split(node, "."))-1])
		} else {
			// Package node
			n.Attr("shape", "box")
			n.Attr("style", "rounded,filled")
			n.Attr("fillcolor", "lightblue")
			n.Attr("label", node)
		}
	}

	// Add edges
	for source, targets := range graph {
		if packagesOnly && strings.Contains(source, ".") && !strings.HasPrefix(source, "cozystack.") {
			// Skip component edges when packages-only is enabled
			continue
		}

		for _, target := range targets {
			if packagesOnly && strings.Contains(target, ".") && !strings.HasPrefix(target, "cozystack.") {
				// Skip component edges when packages-only is enabled
				continue
			}

			// Only add edge if both nodes exist
			if allNodes[source] && allNodes[target] {
				g.Edge(g.Node(source), g.Node(target))
			}
		}
	}

	return g
}

