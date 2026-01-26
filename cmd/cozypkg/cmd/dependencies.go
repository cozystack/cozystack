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

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/emicklei/dot"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
		graph, allNodes, edgeVariants, packageNames, err := buildGraphFromCluster(ctx, dotCmdFlags.kubeconfig, packagesOnly, dotCmdFlags.installed, packageName, selectedPackages)
		if err != nil {
			return fmt.Errorf("error getting PackageSource dependencies: %w", err)
		}

		dotGraph := generateDOTGraph(graph, allNodes, packagesOnly, edgeVariants, packageNames)
		dotGraph.Write(os.Stdout)

		return nil
	},
}

func init() {
	rootCmd.AddCommand(dotCmd)
	dotCmd.Flags().BoolVarP(&dotCmdFlags.installed, "installed", "i", false, "show dependencies only for installed Package resources")
	dotCmd.Flags().BoolVar(&dotCmdFlags.components, "components", false, "show component-level dependencies")
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
// Returns: graph, allNodes, edgeVariants (map[edgeKey]variants), packageNames, error
func buildGraphFromCluster(ctx context.Context, kubeconfig string, packagesOnly bool, installedOnly bool, packageName string, selectedPackages []string) (map[string][]string, map[string]bool, map[string][]string, map[string]bool, error) {
	// Create Kubernetes client config
	var config *rest.Config
	var err error

	if kubeconfig != "" {
		// Load kubeconfig from explicit path
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("failed to load kubeconfig from %s: %w", kubeconfig, err)
		}
	} else {
		// Use default kubeconfig loading (from env var or ~/.kube/config)
		config, err = ctrl.GetConfig()
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("failed to get kubeconfig: %w", err)
		}
	}

	k8sClient, err := client.New(config, client.Options{Scheme: dependenciesScheme})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to create k8s client: %w", err)
	}

	// Get installed Packages if needed
	installedPackages := make(map[string]bool)
	if installedOnly {
		var packageList cozyv1alpha1.PackageList
		if err := k8sClient.List(ctx, &packageList); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("failed to list Packages: %w", err)
		}
		for _, pkg := range packageList.Items {
			installedPackages[pkg.Name] = true
		}
	}

	// List all PackageSource resources
	var packageSourceList cozyv1alpha1.PackageSourceList
	if err := k8sClient.List(ctx, &packageSourceList); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to list PackageSources: %w", err)
	}

	// Build map of existing packages and components
	packageNames := make(map[string]bool)
	allExistingComponents := make(map[string]bool) // "package.component" -> true
	for _, ps := range packageSourceList.Items {
		if ps.Name != "" {
			packageNames[ps.Name] = true
			for _, variant := range ps.Spec.Variants {
				for _, component := range variant.Components {
					if component.Install != nil {
						componentFullName := fmt.Sprintf("%s.%s", ps.Name, component.Name)
						allExistingComponents[componentFullName] = true
					}
				}
			}
		}
	}

	graph := make(map[string][]string)
	allNodes := make(map[string]bool)
	edgeVariants := make(map[string][]string)      // key: "source->target", value: list of variant names
	existingEdges := make(map[string]bool)         // key: "source->target" to avoid duplicates
	componentHasLocalDeps := make(map[string]bool) // componentName -> has local component dependencies

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

		// Track package dependencies per variant
		packageDepVariants := make(map[string]map[string]bool) // dep -> variant -> true
		allVariantNames := make(map[string]bool)
		for _, v := range ps.Spec.Variants {
			allVariantNames[v.Name] = true
		}

		// Track component dependencies per variant
		componentDepVariants := make(map[string]map[string]map[string]bool) // componentName -> dep -> variant -> true
		componentVariants := make(map[string]map[string]bool)               // componentName -> variant -> true

		// Extract dependencies from variants
		for _, variant := range ps.Spec.Variants {
			// Variant-level dependencies (package-level)
			for _, dep := range variant.DependsOn {
				// If installedOnly is set, only include dependencies that are installed
				if installedOnly && !installedPackages[dep] {
					continue
				}

				// Track which variant this dependency comes from
				if packageDepVariants[dep] == nil {
					packageDepVariants[dep] = make(map[string]bool)
				}
				packageDepVariants[dep][variant.Name] = true

				edgeKey := fmt.Sprintf("%s->%s", psName, dep)
				if !existingEdges[edgeKey] {
					graph[psName] = append(graph[psName], dep)
					existingEdges[edgeKey] = true
				}

				// Add to allNodes only if package exists
				if packageNames[dep] {
					allNodes[dep] = true
				}
				// If package doesn't exist, don't add to allNodes - it will be shown as missing (red)
			}

			// Component-level dependencies
			if !packagesOnly {
				for _, component := range variant.Components {
					// Skip components without install section
					if component.Install == nil {
						continue
					}

					componentName := fmt.Sprintf("%s.%s", psName, component.Name)
					allNodes[componentName] = true

					// Track which variants this component appears in
					if componentVariants[componentName] == nil {
						componentVariants[componentName] = make(map[string]bool)
					}
					componentVariants[componentName][variant.Name] = true

					if component.Install != nil {
						if componentDepVariants[componentName] == nil {
							componentDepVariants[componentName] = make(map[string]map[string]bool)
						}

						for _, dep := range component.Install.DependsOn {
							// Track which variant this dependency comes from
							if componentDepVariants[componentName][dep] == nil {
								componentDepVariants[componentName][dep] = make(map[string]bool)
							}
							componentDepVariants[componentName][dep][variant.Name] = true

							// Check if it's a local component dependency or external
							if strings.Contains(dep, ".") {
								// External component dependency (package.component format)
								// Mark that this component has local dependencies (for edge to package logic)
								componentHasLocalDeps[componentName] = true

								// Check if target component exists
								if allExistingComponents[dep] {
									// Component exists
									edgeKey := fmt.Sprintf("%s->%s", componentName, dep)
									if !existingEdges[edgeKey] {
										graph[componentName] = append(graph[componentName], dep)
										existingEdges[edgeKey] = true
									}
									allNodes[dep] = true
								} else {
									// Component doesn't exist - create missing component node
									edgeKey := fmt.Sprintf("%s->%s", componentName, dep)
									if !existingEdges[edgeKey] {
										graph[componentName] = append(graph[componentName], dep)
										existingEdges[edgeKey] = true
									}
									// Don't add to allNodes - will be shown as missing (red)

									// Add edge from missing component to its package
									parts := strings.SplitN(dep, ".", 2)
									if len(parts) == 2 {
										depPackageName := parts[0]
										missingEdgeKey := fmt.Sprintf("%s->%s", dep, depPackageName)
										if !existingEdges[missingEdgeKey] {
											graph[dep] = append(graph[dep], depPackageName)
											existingEdges[missingEdgeKey] = true
										}
										// Add package to allNodes only if it exists
										if packageNames[depPackageName] {
											allNodes[depPackageName] = true
										}
										// If package doesn't exist, it will be shown as missing (red)
									}
								}
							} else {
								// Local component dependency (same package)
								// Mark that this component has local dependencies
								componentHasLocalDeps[componentName] = true

								localDep := fmt.Sprintf("%s.%s", psName, dep)

								// Check if target component exists
								if allExistingComponents[localDep] {
									// Component exists
									edgeKey := fmt.Sprintf("%s->%s", componentName, localDep)
									if !existingEdges[edgeKey] {
										graph[componentName] = append(graph[componentName], localDep)
										existingEdges[edgeKey] = true
									}
									allNodes[localDep] = true
								} else {
									// Component doesn't exist - create missing component node
									edgeKey := fmt.Sprintf("%s->%s", componentName, localDep)
									if !existingEdges[edgeKey] {
										graph[componentName] = append(graph[componentName], localDep)
										existingEdges[edgeKey] = true
									}
									// Don't add to allNodes - will be shown as missing (red)

									// Add edge from missing component to its package
									missingEdgeKey := fmt.Sprintf("%s->%s", localDep, psName)
									if !existingEdges[missingEdgeKey] {
										graph[localDep] = append(graph[localDep], psName)
										existingEdges[missingEdgeKey] = true
									}
								}
							}
						}
					}
				}
			}
		}

		// Store variant information for package dependencies that are not in all variants
		for dep, variants := range packageDepVariants {
			if len(variants) < len(allVariantNames) {
				var variantList []string
				for v := range variants {
					variantList = append(variantList, v)
				}
				edgeKey := fmt.Sprintf("%s->%s", psName, dep)
				edgeVariants[edgeKey] = variantList
			}
		}

		// Add component->package edges for components without local dependencies
		if !packagesOnly {
			for componentName := range componentVariants {
				// Only add edge to package if component has no local component dependencies
				if !componentHasLocalDeps[componentName] {
					edgeKey := fmt.Sprintf("%s->%s", componentName, psName)
					if !existingEdges[edgeKey] {
						graph[componentName] = append(graph[componentName], psName)
						existingEdges[edgeKey] = true
					}
					
					// If component is not in all variants, store variant info for component->package edge
					componentAllVariants := componentVariants[componentName]
					if len(componentAllVariants) < len(allVariantNames) {
						var variantList []string
						for v := range componentAllVariants {
							variantList = append(variantList, v)
						}
						edgeVariants[edgeKey] = variantList
					}
				}
			}
		}

		// Store variant information for component dependencies that are not in all variants
		for componentName, deps := range componentDepVariants {
			componentAllVariants := componentVariants[componentName]
			for dep, variants := range deps {
				if len(variants) < len(componentAllVariants) {
					var variantList []string
					for v := range variants {
						variantList = append(variantList, v)
					}
					// Determine the actual target name
					var targetName string
					if strings.Contains(dep, ".") {
						targetName = dep
					} else {
						targetName = fmt.Sprintf("%s.%s", psName, dep)
					}
					edgeKey := fmt.Sprintf("%s->%s", componentName, targetName)
					edgeVariants[edgeKey] = variantList
				}
			}
		}
	}

	return graph, allNodes, edgeVariants, packageNames, nil
}

// generateDOTGraph generates a DOT graph from the dependency graph.
func generateDOTGraph(graph map[string][]string, allNodes map[string]bool, packagesOnly bool, edgeVariants map[string][]string, packageNames map[string]bool) *dot.Graph {
	g := dot.NewGraph(dot.Directed)
	g.Attr("rankdir", "RL")
	g.Attr("nodesep", "0.5")
	g.Attr("ranksep", "1.0")

	// Helper function to check if a node is a package
	// A node is a package if:
	// 1. It's directly in packageNames
	// 2. It doesn't contain a dot (simple package name)
	// 3. It contains a dot but the part before the first dot is a package name
	isPackage := func(nodeName string) bool {
		if packageNames[nodeName] {
			return true
		}
		if !strings.Contains(nodeName, ".") {
			return true
		}
		// If it contains a dot, check if the part before the first dot is a package
		parts := strings.SplitN(nodeName, ".", 2)
		if len(parts) > 0 {
			return packageNames[parts[0]]
		}
		return false
	}

	// Add nodes
	for node := range allNodes {
		if packagesOnly && !isPackage(node) {
			// Skip component nodes when packages-only is enabled
			continue
		}

		n := g.Node(node)

		// Style nodes based on type
		if isPackage(node) {
			// Package node
			n.Attr("shape", "box")
			n.Attr("style", "rounded,filled")
			n.Attr("fillcolor", "lightblue")
			n.Attr("label", node)
		} else {
			// Component node
			n.Attr("shape", "box")
			n.Attr("style", "rounded,filled")
			n.Attr("fillcolor", "lightyellow")
			// Extract component name (part after last dot)
			parts := strings.Split(node, ".")
			if len(parts) > 0 {
				n.Attr("label", parts[len(parts)-1])
			} else {
				n.Attr("label", node)
			}
		}
	}

	// Add edges
	for source, targets := range graph {
		if packagesOnly && !isPackage(source) {
			// Skip component edges when packages-only is enabled
			continue
		}

		for _, target := range targets {
			if packagesOnly && !isPackage(target) {
				// Skip component edges when packages-only is enabled
				continue
			}

			// Check if target exists
			targetExists := allNodes[target]

			// Determine edge type for coloring
			sourceIsPackage := isPackage(source)
			targetIsPackage := isPackage(target)

			// Add edge
			edge := g.Edge(g.Node(source), g.Node(target))

			// Set edge color based on type (if target exists)
			if targetExists {
				if sourceIsPackage && targetIsPackage {
					// Package -> Package: black (default)
					edge.Attr("color", "black")
				} else {
					// Component -> Package or Component -> Component: green
					edge.Attr("color", "green")
				}
			}

			// If target doesn't exist, mark it as missing (red color)
			if !targetExists {
				edge.Attr("color", "red")
				edge.Attr("style", "dashed")

				// Also add the missing node with red color
				missingNode := g.Node(target)
				missingNode.Attr("shape", "box")
				missingNode.Attr("style", "rounded,filled,dashed")
				missingNode.Attr("fillcolor", "lightcoral")

				// Determine label based on node type
				if isPackage(target) {
					// Package node
					missingNode.Attr("label", target)
				} else {
					// Component node - extract component name
					parts := strings.Split(target, ".")
					if len(parts) > 0 {
						missingNode.Attr("label", parts[len(parts)-1])
					} else {
						missingNode.Attr("label", target)
					}
				}
			} else {
				// Check if this edge has variant information (dependency not in all variants)
				edgeKey := fmt.Sprintf("%s->%s", source, target)
				if variants, hasVariants := edgeVariants[edgeKey]; hasVariants {
					// Add label with variant names
					edge.Attr("label", strings.Join(variants, ","))
				}
			}
		}
	}

	return g
}
