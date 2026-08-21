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
	"path/filepath"
	"regexp"

	"github.com/spf13/cobra"
)

var initCmdFlags struct {
	name string
	app  string
}

// dns1123Label matches a single RFC-1123 label, the character set both the app
// name and the PackageSource name suffix must live within.
var dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// scaffoldFile is one generated file in the repository skeleton.
type scaffoldFile struct {
	Path    string
	Content string
}

// scaffoldFiles returns the file set for a new External-Apps repository built
// around the PackageSource model. psName is the cluster-scoped PackageSource
// name (e.g. "example.hello"); app is the RFC-1123 app/component name.
//
// The layout mirrors what ValidateRepo expects: a PackageSource under
// packages/core/platform/sources/, an app chart at packages/apps/<app>, and a
// paired -rd chart at packages/system/<app>-rd whose cozyrds/ asset carries an
// ApplicationDefinition referencing the app component's assembled artifact.
func scaffoldFiles(psName, app string) []scaffoldFile {
	art := artifactName(psName, "default", app)
	return []scaffoldFile{
		{
			Path: filepath.Join("packages", "core", "platform", "sources", app+".yaml"),
			Content: fmt.Sprintf(`apiVersion: cozystack.io/v1alpha1
kind: PackageSource
metadata:
  name: %s
spec:
  sourceRef:
    kind: OCIRepository
    name: %s-packages
    namespace: cozy-system
    path: /
  variants:
    - name: default
      components:
        - name: %s
          path: apps/%s
        - name: %s-rd
          path: system/%s-rd
          install:
            namespace: cozy-system
`, psName, app, app, app, app, app),
		},
		{
			Path:    filepath.Join("packages", "apps", app, "Chart.yaml"),
			Content: fmt.Sprintf("apiVersion: v2\nname: %s\ndescription: %s application chart\nversion: 0.1.0\n", app, app),
		},
		{
			Path:    filepath.Join("packages", "apps", app, "values.yaml"),
			Content: "# Default values for the " + app + " chart.\n",
		},
		{
			Path: filepath.Join("packages", "apps", app, "templates", "configmap.yaml"),
			Content: `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  placeholder: "replace this template with your application's resources"
`,
		},
		{
			Path:    filepath.Join("packages", "system", app+"-rd", "Chart.yaml"),
			Content: fmt.Sprintf("apiVersion: v2\nname: %s-rd\ndescription: ApplicationDefinition registration for %s\nversion: 0.1.0\n", app, app),
		},
		{
			Path: filepath.Join("packages", "system", app+"-rd", "templates", "cozyrd.yaml"),
			Content: `{{- range $path, $_ := .Files.Glob "cozyrds/*" }}
---
{{ $.Files.Get $path }}
{{- end }}
`,
		},
		{
			Path: filepath.Join("packages", "system", app+"-rd", "cozyrds", app+".yaml"),
			Content: fmt.Sprintf(`apiVersion: cozystack.io/v1alpha1
kind: ApplicationDefinition
metadata:
  name: %s
spec:
  application:
    kind: %s
    openAPISchema: ""
    plural: %ss
    singular: %s
  release:
    prefix: %s-
    chartRef:
      kind: ExternalArtifact
      name: %s
      namespace: cozy-system
  dashboard:
    singular: %s
    plural: %ss
    category: Other
`, app, capitalize(app), app, app, app, art, capitalize(app), app),
		},
		{
			Path: "README.md",
			Content: fmt.Sprintf(`# %s

An External-Apps repository for [Cozystack](https://cozystack.io), built around
the `+"`PackageSource`"+` model.

## Layout

`+"```text"+`
packages/
  core/platform/sources/%s.yaml   # PackageSource: variants, components, dependsOn
  apps/%s/                        # application Helm chart
  system/%s-rd/                   # ApplicationDefinition registration (cozyrds/)
`+"```"+`

## Publish

`+"```bash"+`
cozypkg validate .
cozypkg push oci://<registry>/%s:<tag>
`+"```"+`

## Connect (one command, on a Cozystack cluster)

`+"```bash"+`
cozypkg tap oci://<registry>/%s:<tag>
`+"```"+`
`, psName, app, app, app, app, app),
		},
	}
}

// writeScaffold writes each scaffold file under dir, refusing to overwrite any
// existing file (so init never clobbers a repository).
func writeScaffold(dir string, files []scaffoldFile) error {
	for _, f := range files {
		full := filepath.Join(dir, f.Path)
		if _, err := os.Stat(full); err == nil {
			return fmt.Errorf("refusing to overwrite existing file %s", full)
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(f.Content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'a' && b[0] <= 'z' {
		b[0] -= 'a' - 'A'
	}
	return string(b)
}

var initCmd = &cobra.Command{
	Use:   "init [directory]",
	Short: "Scaffold a new External-Apps repository",
	Long: `Scaffold a new External-Apps repository built around the PackageSource
model: a PackageSource with one variant and a paired app / -rd component, ready
to validate and push. The generated tree passes 'cozypkg validate' as-is.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := "."
		if len(args) == 1 {
			dir = args[0]
		}
		app := initCmdFlags.app
		if !dns1123Label.MatchString(app) {
			return fmt.Errorf("--app %q must be a valid RFC-1123 label (lowercase alphanumeric and -)", app)
		}
		psName := initCmdFlags.name
		if psName == "" {
			psName = "example." + app
		}

		files := scaffoldFiles(psName, app)
		if err := writeScaffold(dir, files); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Scaffolded %d files under %s\nNext: cozypkg validate %s\n", len(files), dir, dir)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().StringVar(&initCmdFlags.app, "app", "myapp", "Name of the sample app/component (RFC-1123 label)")
	initCmd.Flags().StringVar(&initCmdFlags.name, "name", "", "PackageSource name (defaults to example.<app>)")
}
