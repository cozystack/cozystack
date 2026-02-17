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
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
)

//go:embed manifests/*.yaml
var embeddedCRDManifests embed.FS

// WriteEmbeddedManifests extracts embedded CRD manifests to a directory.
func WriteEmbeddedManifests(dir string) error {
	manifests, err := fs.ReadDir(embeddedCRDManifests, "manifests")
	if err != nil {
		return fmt.Errorf("failed to read embedded manifests: %w", err)
	}

	for _, manifest := range manifests {
		data, err := fs.ReadFile(embeddedCRDManifests, path.Join("manifests", manifest.Name()))
		if err != nil {
			return fmt.Errorf("failed to read file %s: %w", manifest.Name(), err)
		}

		outputPath := path.Join(dir, manifest.Name())
		if err := os.WriteFile(outputPath, data, 0666); err != nil {
			return fmt.Errorf("failed to write file %s: %w", outputPath, err)
		}
	}

	return nil
}
