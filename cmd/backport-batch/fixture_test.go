/*
Copyright 2026 The Cozystack Authors.

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

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fixture is a repository whose origin is a bare repository next to it, with
// main and release lines shaped the way GitHub leaves them: every pull request
// lands as a merge commit whose message is its title, its number and its body.
type fixture struct {
	t      *testing.T
	dir    string
	remote string
	now    time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	// A hook or an outer repository can export these into the test process.
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	// The user's global config may sign every commit or run hooks.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, who := range []string{"AUTHOR", "COMMITTER"} {
		t.Setenv("GIT_"+who+"_NAME", "Batch Test")
		t.Setenv("GIT_"+who+"_EMAIL", "batch@example.invalid")
	}
	root := t.TempDir()
	f := &fixture{t: t, dir: filepath.Join(root, "work"), remote: filepath.Join(root, "remote.git"),
		now: time.Date(2026, 10, 26, 6, 0, 0, 0, time.UTC)}
	for _, dir := range []string{f.remote, f.dir} {
		args := []string{"init", "-q", "-b", "main", dir}
		if dir == f.remote {
			args = []string{"init", "-q", "--bare", "-b", "main", dir}
		}
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Chdir(f.dir)
	f.git("remote", "add", "origin", f.remote)
	f.write("README.md", "seed\n")
	f.write("chart/values.yaml", "replicas: 1\nimage: app:1\n")
	f.git("add", "-A")
	f.git("commit", "-q", "-m", "chore: seed")
	f.git("push", "-q", "origin", "main")
	return f
}

func (f *fixture) git(args ...string) string {
	f.t.Helper()
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *fixture) write(path, content string) {
	f.t.Helper()
	full := filepath.Join(f.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// apply writes files in a stable order; an empty content removes the file.
func (f *fixture) apply(files map[string]string) {
	f.t.Helper()
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if files[k] == "" {
			f.git("rm", "-q", "--", k)
			continue
		}
		f.write(k, files[k])
	}
	f.git("add", "-A")
}

// pr merges pull request n into main as a merge commit and returns it.
func (f *fixture) pr(n int, title, body string, files map[string]string) string {
	f.t.Helper()
	branch := fmt.Sprintf("pr-%d", n)
	f.git("switch", "-q", "main")
	f.git("switch", "-q", "-c", branch)
	f.apply(files)
	f.git("commit", "-q", "-m", fmt.Sprintf("change for #%d", n))
	f.git("switch", "-q", "main")
	f.git("merge", "-q", "--no-ff", "-m", fmt.Sprintf("%s (#%d)\n\n%s", title, n, body), branch)
	f.git("push", "-q", "origin", "main")
	return f.git("rev-parse", "HEAD")
}

// squash merges pull request n into main as a single commit.
func (f *fixture) squash(n int, title, body string, files map[string]string) string {
	f.t.Helper()
	f.git("switch", "-q", "main")
	f.apply(files)
	f.git("commit", "-q", "-m", fmt.Sprintf("%s (#%d)\n\n%s", title, n, body))
	f.git("push", "-q", "origin", "main")
	return f.git("rev-parse", "HEAD")
}

// release cuts a release line at main's head.
func (f *fixture) release(name string) {
	f.t.Helper()
	f.git("push", "-q", "origin", "main:refs/heads/"+name)
}

// onLine commits onto a release line, as a maintainer's merge would land
// there, and returns the commit.
func (f *fixture) onLine(name, message string, files map[string]string) string {
	f.t.Helper()
	f.git("fetch", "-q", "origin")
	f.git("switch", "-q", "--detach", "origin/"+name)
	f.apply(files)
	f.git("commit", "-q", "--allow-empty", "-m", message)
	sha := f.git("rev-parse", "HEAD")
	f.git("push", "-q", "origin", sha+":refs/heads/"+name)
	f.git("switch", "-q", "main")
	return sha
}

// remoteHead is what the bare remote has for a branch, or "".
func (f *fixture) remoteHead(branch string) string {
	f.t.Helper()
	out, err := exec.Command("git", "--git-dir="+f.remote, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// fakeGH stands in for gh: the open pull requests it reports, and what the
// tool asked it to create or edit.
type fakeGH struct {
	prs     []pullRequest
	err     error
	perms   map[string]string
	permErr error
	asked   []string
	created []string
	edited  []string
	bodies  []string
}

// permission answers write for the fixture's maintainer and read for anyone
// else, unless the test says otherwise.
func (g *fakeGH) permission(login string) (string, error) {
	g.asked = append(g.asked, login)
	if g.permErr != nil {
		return "", g.permErr
	}
	if p, ok := g.perms[login]; ok {
		return p, nil
	}
	if login == "maintainer" {
		return "write", nil
	}
	return "read", nil
}

func (g *fakeGH) openPulls(base string) ([]pullRequest, error) {
	var out []pullRequest
	for _, pr := range g.prs {
		if pr.Base.Ref == base {
			out = append(out, pr)
		}
	}
	return out, g.err
}

func (g *fakeGH) createPull(base, head, title, bodyFile string) error {
	g.created = append(g.created, base+" "+head+" "+title)
	return g.readBody(bodyFile)
}

func (g *fakeGH) editPull(number int, title, bodyFile string) error {
	g.edited = append(g.edited, fmt.Sprintf("#%d %s", number, title))
	return g.readBody(bodyFile)
}

func (g *fakeGH) readBody(path string) error {
	data, err := os.ReadFile(path)
	g.bodies = append(g.bodies, string(data))
	return err
}

// batchPR is an open batch pull request opened by a maintainer from a branch
// of this repository, at whatever head the remote has for the branch.
func (f *fixture) batchPR(n int, base, branch, body string) pullRequest {
	f.t.Helper()
	var pr pullRequest
	pr.Number, pr.Body = n, body
	pr.HTMLURL = fmt.Sprintf("https://github.com/%s/pull/%d", upstreamRepo, n)
	pr.User.Login, pr.User.Type, pr.AuthorAssociation = "maintainer", "User", "MEMBER"
	pr.Head.Ref, pr.Head.SHA = branch, f.remoteHead(branch)
	pr.Head.Repo = &struct {
		FullName string `json:"full_name"`
	}{upstreamRepo}
	pr.Base.Ref, pr.Base.Repo.FullName = base, upstreamRepo
	return pr
}

// exec runs the tool in the fixture with gh faked, on the fixture's date.
func (f *fixture) exec(gh *fakeGH, args ...string) (int, string, string) {
	f.t.Helper()
	if gh == nil {
		gh = &fakeGH{}
	}
	var stdout, stderr bytes.Buffer
	a := &app{gh: gh, stdout: &stdout, stderr: &stderr, now: func() time.Time { return f.now }}
	code := a.main(args)
	return code, stdout.String(), stderr.String()
}

const batchBranch1 = "backport-batch/release-1.1-20261026"

// lines cuts release-1.0 and then release-1.1 with one change between them,
// leaving release-1.1 the newer of the two supported lines.
func (f *fixture) lines() {
	f.t.Helper()
	f.release("release-1.0")
	f.pr(1, "feat(app): something for 1.1", "", map[string]string{"app/feature.txt": "1.1\n"})
	f.release("release-1.1")
}

// subjects lists the subjects of the commits on branch beyond base, oldest
// first.
func (f *fixture) subjects(base, branch string) []string {
	f.t.Helper()
	out := f.git("log", "--reverse", "--format=%s", base+".."+branch)
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}
