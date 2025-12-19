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

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/emicklei/dot"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	psdsDir      string
	outputFile   string
	packagesOnly bool
	format       string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "psds-graph",
		Short: "Generate dependency graph for PackageSource files",
		Long: `Generate a dependency graph visualization for PackageSource files.

The command scans all YAML files in the specified directory, extracts dependencies,
and generates a graph in DOT format (compatible with graphviz).

Examples:
  # Generate SVG graph
  psds-graph --psds-dir packages/core/platform/psds --output dependencies.svg

  # Generate PNG (requires graphviz)
  psds-graph --psds-dir packages/core/platform/psds --output dependencies.png --format png

  # Show only package-level dependencies
  psds-graph --psds-dir packages/core/platform/psds --output dependencies.svg --packages-only`,
		RunE: run,
	}

	rootCmd.Flags().StringVar(&psdsDir, "psds-dir", "packages/core/platform/psds", "Directory containing PackageSource YAML files")
	rootCmd.Flags().StringVar(&outputFile, "output", "dependencies.dot", "Output file path")
	rootCmd.Flags().BoolVar(&packagesOnly, "packages-only", false, "Show only package-level dependencies, hide component dependencies")
	rootCmd.Flags().StringVar(&format, "format", "dot", "Output format: dot, svg, png, pdf (requires graphviz for non-dot formats)")

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(cmd *cobra.Command, args []string) error {
	if psdsDir == "" {
		return fmt.Errorf("--psds-dir is required")
	}

	fmt.Fprintf(os.Stderr, "Scanning %s...\n", psdsDir)

	graph, allNodes, err := buildGraph(psdsDir, packagesOnly)
	if err != nil {
		return fmt.Errorf("failed to build graph: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Found %d nodes and %d edges\n", len(allNodes), countEdges(graph))

	// Generate DOT graph
	dotGraph := generateDOTGraph(graph, allNodes, packagesOnly)

	// Write DOT file first
	dotFile := outputFile
	if format != "dot" {
		dotFile = strings.TrimSuffix(outputFile, filepath.Ext(outputFile)) + ".dot"
	}

	if err := writeDOT(dotGraph, dotFile); err != nil {
		return err
	}

	// If format is not dot, try to render using graphviz
	if format != "dot" {
		return renderWithGraphviz(dotFile, outputFile, format)
	}

	return nil
}

type PackageSource struct {
	APIVersion string                 `yaml:"apiVersion"`
	Kind       string                 `yaml:"kind"`
	Metadata   map[string]interface{} `yaml:"metadata"`
	Spec       Spec                   `yaml:"spec"`
}

type Spec struct {
	Variants []Variant `yaml:"variants"`
}

type Variant struct {
	Name       string     `yaml:"name"`
	DependsOn  []string   `yaml:"dependsOn"`
	Components []Component `yaml:"components"`
}

type Component struct {
	Name   string      `yaml:"name"`
	Install Install    `yaml:"install"`
}

type Install struct {
	DependsOn []string `yaml:"dependsOn"`
}

func buildGraph(psdsDir string, packagesOnly bool) (map[string][]string, map[string]bool, error) {
	graph := make(map[string][]string)
	allNodes := make(map[string]bool)

	err := filepath.Walk(psdsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !strings.HasSuffix(path, ".yaml") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to read %s: %v\n", path, err)
			return nil
		}

		var ps PackageSource
		if err := yaml.Unmarshal(data, &ps); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to parse %s: %v\n", path, err)
			return nil
		}

		packageName, ok := ps.Metadata["name"].(string)
		if !ok || packageName == "" {
			fmt.Fprintf(os.Stderr, "Warning: %s has no package name, skipping\n", path)
			return nil
		}

		allNodes[packageName] = true

		// Extract dependencies
		for _, variant := range ps.Spec.Variants {
			// Variant-level dependencies
			for _, dep := range variant.DependsOn {
				graph[packageName] = append(graph[packageName], dep)
				allNodes[dep] = true
			}

			// Component-level dependencies
			if !packagesOnly {
				for _, component := range variant.Components {
					componentName := fmt.Sprintf("%s.%s", packageName, component.Name)
					allNodes[componentName] = true

					for _, dep := range component.Install.DependsOn {
						// Check if it's a local component dependency or external
						if strings.Contains(dep, ".") {
							graph[componentName] = append(graph[componentName], dep)
							allNodes[dep] = true
						} else {
							// Local component dependency
							localDep := fmt.Sprintf("%s.%s", packageName, dep)
							graph[componentName] = append(graph[componentName], localDep)
							allNodes[localDep] = true
						}
					}
				}
			}
		}

		return nil
	})

	return graph, allNodes, err
}

func countEdges(graph map[string][]string) int {
	count := 0
	for _, deps := range graph {
		count += len(deps)
	}
	return count
}

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
			label := node
			if strings.HasPrefix(node, "cozystack.") {
				label = strings.TrimPrefix(node, "cozystack.")
			}
			n.Attr("label", label)
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

func writeDOT(g *dot.Graph, outputFile string) error {
	file, err := os.Create(outputFile)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer file.Close()

	g.Write(file)

	return nil
}

func renderWithGraphviz(dotFile, outputFile, format string) error {
	// Check if dot is available
	dotPath, err := exec.LookPath("dot")
	if err != nil {
		return fmt.Errorf("graphviz 'dot' command not found. Install with: brew install graphviz\nGenerated DOT file: %s", dotFile)
	}

	cmd := exec.Command(dotPath, "-T"+format, dotFile, "-o", outputFile)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to render graph: %w\nGenerated DOT file: %s", err, dotFile)
	}

	fmt.Fprintf(os.Stderr, "Generated %s\n", outputFile)
	return nil
}

