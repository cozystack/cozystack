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

package crdinstall

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Install applies Cozystack CRDs using embedded manifests.
// It extracts the manifests and applies them to the cluster using server-side apply.
func Install(ctx context.Context, k8sClient client.Client, writeEmbeddedManifests func(string) error) error {
	logger := log.FromContext(ctx)

	tmpDir, err := os.MkdirTemp("", "crd-install-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	manifestsDir := filepath.Join(tmpDir, "manifests")
	if err := os.MkdirAll(manifestsDir, 0755); err != nil {
		return fmt.Errorf("failed to create manifests directory: %w", err)
	}

	if err := writeEmbeddedManifests(manifestsDir); err != nil {
		return fmt.Errorf("failed to extract embedded manifests: %w", err)
	}

	entries, err := os.ReadDir(manifestsDir)
	if err != nil {
		return fmt.Errorf("failed to read manifests directory: %w", err)
	}

	var manifestFiles []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".yaml") {
			manifestFiles = append(manifestFiles, filepath.Join(manifestsDir, entry.Name()))
		}
	}

	if len(manifestFiles) == 0 {
		return fmt.Errorf("no YAML manifest files found in directory")
	}

	var objects []*unstructured.Unstructured
	for _, manifestPath := range manifestFiles {
		objs, err := parseManifests(manifestPath)
		if err != nil {
			return fmt.Errorf("failed to parse manifests from %s: %w", manifestPath, err)
		}
		objects = append(objects, objs...)
	}

	if len(objects) == 0 {
		return fmt.Errorf("no objects found in manifests")
	}

	logger.Info("Applying Cozystack CRDs", "count", len(objects))
	for _, obj := range objects {
		patchOptions := &client.PatchOptions{
			FieldManager: "cozystack-operator",
			Force:        func() *bool { b := true; return &b }(),
		}

		if err := k8sClient.Patch(ctx, obj, client.Apply, patchOptions); err != nil {
			return fmt.Errorf("failed to apply CRD %s: %w", obj.GetName(), err)
		}
		logger.Info("Applied CRD", "name", obj.GetName())
	}

	if err := waitForCRDsEstablished(ctx, k8sClient, objects, logger); err != nil {
		return fmt.Errorf("CRDs not established after apply: %w", err)
	}

	logger.Info("CRD installation completed successfully")
	return nil
}

// waitForCRDsEstablished polls applied CRDs until all have the Established condition.
func waitForCRDsEstablished(ctx context.Context, k8sClient client.Client, objects []*unstructured.Unstructured, logger interface{ Info(string, ...interface{}) }) error {
	var crdNames []string
	for _, obj := range objects {
		if obj.GetKind() == "CustomResourceDefinition" {
			crdNames = append(crdNames, obj.GetName())
		}
	}

	if len(crdNames) == 0 {
		return nil
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		allEstablished := true
		for _, name := range crdNames {
			crd := &unstructured.Unstructured{}
			crd.SetGroupVersionKind(objects[0].GroupVersionKind())
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, crd); err != nil {
				allEstablished = false
				break
			}

			conditions, found, err := unstructured.NestedSlice(crd.Object, "status", "conditions")
			if err != nil || !found {
				allEstablished = false
				break
			}

			established := false
			for _, c := range conditions {
				cond, ok := c.(map[string]interface{})
				if !ok {
					continue
				}
				if cond["type"] == "Established" && cond["status"] == "True" {
					established = true
					break
				}
			}
			if !established {
				allEstablished = false
				break
			}
		}

		if allEstablished {
			logger.Info("All CRDs established", "count", len(crdNames))
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while waiting for CRDs to be established: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func parseManifests(manifestPath string) ([]*unstructured.Unstructured, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest file: %w", err)
	}

	return readYAMLObjects(bytes.NewReader(data))
}

func readYAMLObjects(reader io.Reader) ([]*unstructured.Unstructured, error) {
	var objects []*unstructured.Unstructured
	yamlReader := k8syaml.NewYAMLReader(bufio.NewReader(reader))

	for {
		doc, err := yamlReader.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("failed to read YAML document: %w", err)
		}

		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}

		obj := &unstructured.Unstructured{}
		decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(doc), len(doc))
		if err := decoder.Decode(obj); err != nil {
			if err == io.EOF {
				continue
			}
			return nil, fmt.Errorf("failed to decode YAML document: %w", err)
		}

		if obj.GetKind() == "" {
			continue
		}

		objects = append(objects, obj)
	}

	return objects, nil
}
