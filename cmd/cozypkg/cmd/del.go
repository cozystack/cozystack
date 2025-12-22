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

		// Delete each package
		for packageName := range packageNames {
			pkg := &cozyv1alpha1.Package{}
			pkg.Name = packageName
			if err := k8sClient.Delete(ctx, pkg); err != nil {
				if client.IgnoreNotFound(err) == nil {
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

func init() {
	rootCmd.AddCommand(delCmd)
	delCmd.Flags().StringArrayVarP(&delCmdFlags.files, "file", "f", []string{}, "Read packages from file or directory (can be specified multiple times)")
	delCmd.Flags().StringVar(&delCmdFlags.kubeconfig, "kubeconfig", "", "Path to kubeconfig file (defaults to ~/.kube/config or KUBECONFIG env var)")
}

