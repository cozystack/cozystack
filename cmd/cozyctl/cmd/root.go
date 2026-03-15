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
	"os"

	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags.
var Version = "dev"

var globalFlags struct {
	kubeconfig string
	context    string
	namespace  string
}

var rootCmd = &cobra.Command{
	Use:               "cozyctl",
	Short:             "A CLI for managing Cozystack applications",
	SilenceErrors:     true,
	SilenceUsage:      true,
	DisableAutoGenTag: true,
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() error {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return err
	}
	return nil
}

func init() {
	rootCmd.Version = Version
	rootCmd.PersistentFlags().StringVar(&globalFlags.kubeconfig, "kubeconfig", "", "Path to kubeconfig file")
	rootCmd.PersistentFlags().StringVar(&globalFlags.context, "context", "", "Kubernetes context to use")
	rootCmd.PersistentFlags().StringVarP(&globalFlags.namespace, "namespace", "n", "", "Kubernetes namespace")
}
