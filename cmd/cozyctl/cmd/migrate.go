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
	"github.com/spf13/cobra"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate <type> <name>",
	Short: "Live-migrate a VirtualMachine to another node",
	Long:  `Live-migrate a VirtualMachine to another node using virtctl. Only valid for VirtualMachine or VMInstance kinds.`,
	Args:  cobra.ExactArgs(2),
	RunE:  runMigrate,
}

func init() {
	rootCmd.AddCommand(migrateCmd)
}

func runMigrate(cmd *cobra.Command, args []string) error {
	vmName, ns, err := resolveVMArgs(args)
	if err != nil {
		return err
	}

	virtctlArgs := []string{"virtctl", "migrate", vmName, "-n", ns}
	return execVirtctl(virtctlArgs)
}
