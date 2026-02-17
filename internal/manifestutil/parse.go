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

package manifestutil

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

// ParseManifestFile reads a YAML file and parses it into unstructured objects.
func ParseManifestFile(manifestPath string) ([]*unstructured.Unstructured, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest file: %w", err)
	}

	return ReadYAMLObjects(bytes.NewReader(data))
}

// ReadYAMLObjects parses multi-document YAML from a reader into unstructured objects.
// Empty documents and documents without a kind are skipped.
func ReadYAMLObjects(reader io.Reader) ([]*unstructured.Unstructured, error) {
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
