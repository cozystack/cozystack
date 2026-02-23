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
	"os/exec"
	"strings"
	"syscall"
)

// vmKindPrefix maps application Kind to the release prefix used by KubeVirt VMs.
func vmKindPrefix(kind string) (string, bool) {
	switch kind {
	case "VirtualMachine":
		return "virtual-machine", true
	case "VMInstance":
		return "vm-instance", true
	default:
		return "", false
	}
}

// resolveVMArgs takes CLI args (type, name or type/name), resolves the application type
// via discovery, validates it's a VM kind, and returns the full VM name and namespace.
func resolveVMArgs(args []string) (string, string, error) {
	var resourceType, resourceName string

	if len(args) == 1 {
		// type/name format
		parts := strings.SplitN(args[0], "/", 2)
		if len(parts) != 2 {
			return "", "", fmt.Errorf("expected type/name format, got %q", args[0])
		}
		resourceType, resourceName = parts[0], parts[1]
	} else {
		resourceType = args[0]
		resourceName = args[1]
	}

	ctx := context.Background()
	typedClient, _, err := newClients()
	if err != nil {
		return "", "", err
	}

	registry, err := discoverAppDefs(ctx, typedClient)
	if err != nil {
		return "", "", err
	}

	info := registry.Resolve(resourceType)
	if info == nil {
		return "", "", fmt.Errorf("unknown application type %q", resourceType)
	}

	prefix, ok := vmKindPrefix(info.Kind)
	if !ok {
		return "", "", fmt.Errorf("resource type %q (Kind=%s) is not a VirtualMachine or VMInstance", resourceType, info.Kind)
	}

	ns, err := getNamespace()
	if err != nil {
		return "", "", err
	}

	vmName := prefix + "-" + resourceName
	return vmName, ns, nil
}

// execVirtctl replaces the current process with virtctl.
func execVirtctl(args []string) error {
	virtctlPath, err := exec.LookPath("virtctl")
	if err != nil {
		return fmt.Errorf("virtctl not found in PATH: %w", err)
	}

	// Append kubeconfig/context flags if set
	if globalFlags.kubeconfig != "" {
		args = append(args, "--kubeconfig", globalFlags.kubeconfig)
	}
	if globalFlags.context != "" {
		args = append(args, "--context", globalFlags.context)
	}

	if err := syscall.Exec(virtctlPath, args, os.Environ()); err != nil {
		return fmt.Errorf("failed to exec virtctl: %w", err)
	}
	return nil
}
