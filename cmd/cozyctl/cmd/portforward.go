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

	"github.com/spf13/cobra"
)

var portForwardCmd = &cobra.Command{
	Use:   "port-forward <type/name> [ports...]",
	Short: "Forward ports to a VirtualMachineInstance",
	Long:  `Forward ports to a VirtualMachineInstance using virtctl. Only valid for VirtualMachine or VMInstance kinds.`,
	Args:  cobra.MinimumNArgs(2),
	RunE:  runPortForward,
}

func init() {
	rootCmd.AddCommand(portForwardCmd)
}

func runPortForward(cmd *cobra.Command, args []string) error {
	vmName, ns, err := resolveVMArgs(args[:1])
	if err != nil {
		return err
	}

	ports := args[1:]
	if len(ports) == 0 {
		return fmt.Errorf("at least one port is required")
	}

	virtctlArgs := []string{"virtctl", "port-forward", "vmi/" + vmName, "-n", ns}
	virtctlArgs = append(virtctlArgs, ports...)
	return execVirtctl(virtctlArgs)
}
