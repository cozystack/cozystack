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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitScaffoldValidatesClean is the dogfood property: whatever `cozypkg
// init` generates must pass `cozypkg validate` with zero findings. If the
// scaffold and the validator ever disagree on the repository format, this
// fails.
func TestInitScaffoldValidatesClean(t *testing.T) {
	dir := t.TempDir()
	if err := writeScaffold(dir, scaffoldFiles("example.hello", "hello")); err != nil {
		t.Fatalf("writeScaffold: %v", err)
	}
	rep, err := ValidateRepo(ValidateOptions{RepoRoot: dir})
	if err != nil {
		t.Fatalf("ValidateRepo: %v", err)
	}
	if len(rep.Findings) != 0 {
		t.Fatalf("scaffold must validate with zero findings, got: %+v", rep.Findings)
	}
}

func TestWriteScaffoldRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	files := scaffoldFiles("example.hello", "hello")
	if err := writeScaffold(dir, files); err != nil {
		t.Fatalf("first writeScaffold: %v", err)
	}
	if err := writeScaffold(dir, files); err == nil {
		t.Fatal("expected second writeScaffold to refuse overwriting existing files")
	}
}

func TestCapitalizeYieldsValidKind(t *testing.T) {
	cases := map[string]string{
		"myapp":       "Myapp",
		"foo-bar":     "FooBar",
		"a-b-c":       "ABC",
		"minecraft":   "Minecraft",
		"redis-cache": "RedisCache",
	}
	for in, want := range cases {
		if got := capitalize(in); got != want {
			t.Errorf("capitalize(%q) = %q, want %q", in, got, want)
		}
		// A Kubernetes kind must not contain hyphens.
		if strings.Contains(capitalize(in), "-") {
			t.Errorf("capitalize(%q) = %q contains a hyphen (invalid kind)", in, capitalize(in))
		}
	}
}

func TestInitRejectsReservedName(t *testing.T) {
	// Driven off reservedNamePrefixes rather than a literal list, so a prefix
	// added for `validate` cannot leave `init` quietly letting it through.
	var names []string
	for _, p := range reservedNamePrefixes {
		names = append(names, p+"foo", p+"acme.demo")
	}
	for _, name := range names {
		initCmdFlags.app = "demo"
		initCmdFlags.name = name
		err := initCmd.RunE(initCmd, []string{t.TempDir()})
		if err == nil || !strings.Contains(err.Error(), "reserved prefix") {
			t.Errorf("init --name %q should be rejected for reserved prefix, got %v", name, err)
		}
	}
	initCmdFlags.name = ""
}

func TestDNS1123LabelGuard(t *testing.T) {
	good := []string{"myapp", "foo-bar", "a", "app123"}
	bad := []string{"MyApp", "foo_bar", "-foo", "foo-", "foo.bar", ""}
	for _, s := range good {
		if !dns1123Label.MatchString(s) {
			t.Errorf("expected %q to be a valid label", s)
		}
	}
	for _, s := range bad {
		if dns1123Label.MatchString(s) {
			t.Errorf("expected %q to be rejected", s)
		}
	}
}

func TestBuildFluxPushArgs(t *testing.T) {
	got := buildFluxPushArgs("oci://reg/example:v1", "/repo/packages", "https://example.git", "v1:abc", false)
	want := []string{
		"push", "artifact", "oci://reg/example:v1",
		"--path=/repo/packages",
		"--source=https://example.git",
		"--revision=v1:abc",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("args = %v, want %v", got, want)
	}
	if withRepro := buildFluxPushArgs("oci://r/e:v1", "/p", "s", "r", true); withRepro[len(withRepro)-1] != "--reproducible" {
		t.Fatalf("expected --reproducible as last arg, got %v", withRepro)
	}
}

// gitInit sets up a throwaway git repo with a remote and one commit. Signing is
// disabled locally (config, not the forbidden --no-gpg-sign flag) because this
// is a disposable test fixture, never a project commit.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init")
	run("remote", "add", "origin", "https://example.com/repo.git")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	run("config", "commit.gpgsign", "false")
	writeFile(t, dir, "file.txt", "x")
	run("add", ".")
	run("commit", "--message", "initial")
}

func TestDeriveSourceAndRevision(t *testing.T) {
	dir := t.TempDir()
	if got := deriveSource(dir); got != "" {
		t.Fatalf("expected empty source for non-git dir, got %q", got)
	}
	if got := deriveRevision(dir); got != "" {
		t.Fatalf("expected empty revision for non-git dir, got %q", got)
	}

	gitInit(t, dir)
	if got := deriveSource(dir); got != "https://example.com/repo.git" {
		t.Fatalf("deriveSource = %q", got)
	}
	rev := deriveRevision(dir)
	if rev == "" {
		t.Fatal("deriveRevision returned empty for a git repo with a commit")
	}
	// A 40-hex sha must be present either standalone or after "<describe>:".
	sha := rev
	if i := strings.LastIndex(rev, ":"); i >= 0 {
		sha = rev[i+1:]
	}
	if len(sha) != 40 {
		t.Fatalf("expected a 40-char sha in revision %q, got %q", rev, sha)
	}
}

func TestScaffoldAppDefArtifactNameLink(t *testing.T) {
	// A tapped repository keeps its declared name, so the scaffold references the
	// component's assembled artifact by its concrete name (no tap-time
	// templating). For example.hello/default/hello that name is
	// example-hello-default-hello.
	files := scaffoldFiles("example.hello", "hello")
	var rdContent, cozyrdTemplate string
	for _, f := range files {
		if strings.Contains(f.Path, filepath.Join("cozyrds", "hello.yaml")) {
			rdContent = f.Content
		}
		if strings.HasSuffix(f.Path, filepath.Join("templates", "cozyrd.yaml")) {
			cozyrdTemplate = f.Content
		}
	}
	if rdContent == "" {
		t.Fatal("scaffold has no cozyrds/hello.yaml")
	}
	if !strings.Contains(rdContent, "name: example-hello-default-hello") {
		t.Fatalf("cozyrds chartRef must reference the concrete artifact name, content:\n%s", rdContent)
	}
	if strings.Contains(rdContent, "{{") {
		t.Fatalf("cozyrds chartRef must not be templated any more, content:\n%s", rdContent)
	}
	// The -rd chart renders the asset directly; no tpl indirection is needed.
	if !strings.Contains(cozyrdTemplate, "$.Files.Get $path") || strings.Contains(cozyrdTemplate, "tpl ") {
		t.Fatalf("cozyrd.yaml must render the cozyrds asset with a plain Files.Get, content:\n%s", cozyrdTemplate)
	}
}
