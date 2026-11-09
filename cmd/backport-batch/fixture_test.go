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
	"cmp"
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
	// tool is the commit the tool says it was built from.
	tool string
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
	f.write(policyPage, policyWith(""))
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

// joined makes a merge of tip onto base with tip's tree, whatever a merge of
// the two would make.
func (f *fixture) joined(base, tip, message string) string {
	f.t.Helper()
	return f.git("commit-tree", "-p", base, "-p", tip, "-m", message, tip+"^{tree}")
}

// contributor writes the commits of the pull requests that say so.
const contributor = "Contributor One <one@example.invalid>"

// change is one step of a pull request's branch: a commit of files, by author
// with message when given, or with mergeMain a merge of main into the branch
// after main moved on.
type change struct {
	files     map[string]string
	author    string
	message   string
	mergeMain bool
}

// prCommits merges pull request n into main as a merge commit of a branch
// made of changes, and returns the merge commit.
func (f *fixture) prCommits(n int, title, body string, changes ...change) string {
	f.t.Helper()
	branch := fmt.Sprintf("pr-%d", n)
	f.git("switch", "-q", "main")
	f.git("switch", "-q", "-c", branch)
	for i, c := range changes {
		if c.mergeMain {
			f.git("switch", "-q", "main")
			f.apply(map[string]string{fmt.Sprintf("main/%d-%d.txt", n, i): "main moved\n"})
			f.git("commit", "-q", "-m", "chore: move main along")
			f.git("switch", "-q", branch)
			f.git("merge", "-q", "--no-ff", "-m", "Merge branch 'main' into "+branch, "main")
			continue
		}
		f.apply(c.files)
		msg := c.message
		if msg == "" {
			msg = fmt.Sprintf("change %d for #%d", i+1, n)
		}
		args := []string{"commit", "-q", "--allow-empty", "-m", msg}
		if c.author != "" {
			args = append(args, "--author", c.author)
		}
		f.git(args...)
	}
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

// descends says whether tip descends from base, failing the test when git
// cannot tell.
func descends(t *testing.T, base, tip string) bool {
	t.Helper()
	ok, err := isAncestor(base, tip)
	if err != nil {
		t.Fatal(err)
	}
	return ok
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
	labels  map[string][]int
	listErr error
	asked   []string
	created []string
	edited  []string
	bodies  []string
	// issueComments are the comments on the batch pull request, which
	// createComment and editComment change.
	issueComments []issueComment
	// bot is what BACKPORT_BATCH_BOT would say.
	bot string
	// listed counts the pages of recentPulls read.
	listed int
	// closedWrites names every write the tool attempted on a pull request
	// that is no longer open; exec fails the test on one.
	closedWrites []string
	// fail makes the next call of an operation, by its method name, fail.
	fail map[string]error
	// nextPR is the number createPull gives, 90 when unset.
	nextPR int
	// remoteHead reads a branch's head off the fixture's remote: GitHub
	// reports an open pull request at what its branch has.
	remoteHead func(string) string
	// commented lists the pull request each comment went to, in order.
	commented []int
	// after runs once an operation, by its method name, has succeeded: what
	// someone else does on GitHub meanwhile.
	after map[string]func()
}

// live is pr as GitHub reports it now: an open one at the head its branch
// has on the remote.
func (g *fakeGH) live(pr pullRequest) pullRequest {
	if g.remoteHead != nil && (pr.State == "" || pr.State == "open") {
		if head := g.remoteHead(pr.Head.Ref); head != "" {
			pr.Head.SHA = head
		}
	}
	return pr
}

// inject is the failure a test set for the next call of op, once.
func (g *fakeGH) inject(op string) error {
	err := g.fail[op]
	delete(g.fail, op)
	return err
}

func (g *fakeGH) pull(n int) (pullRequest, error) {
	if err := g.inject("pull"); err != nil {
		return pullRequest{}, err
	}
	for _, pr := range g.prs {
		if pr.Number == n {
			return g.live(pr), nil
		}
	}
	return pullRequest{}, fmt.Errorf("HTTP 404: no pull request #%d", n)
}

// isOpen says whether the fake has pull request n open, or does not know it.
func (g *fakeGH) isOpen(n int) bool {
	for _, pr := range g.prs {
		if pr.Number == n {
			return pr.State == "" || pr.State == "open"
		}
	}
	return true
}

// write refuses a write to a pull request that is no longer open, which the
// tool must never attempt.
func (g *fakeGH) write(n int, what string) error {
	if g.isOpen(n) {
		return nil
	}
	g.closedWrites = append(g.closedWrites, fmt.Sprintf("%s on #%d", what, n))
	return fmt.Errorf("#%d is closed", n)
}

// recentPulls pages every pull request against base, newest first, as the
// REST listing sorted by creation does; a higher number is a newer one.
func (g *fakeGH) recentPulls(base string, page int) ([]pullRequest, error) {
	g.listed++
	var all []pullRequest
	for _, pr := range g.prs {
		if pr.Base.Ref == base {
			all = append(all, pr)
		}
	}
	slices.SortFunc(all, func(a, b pullRequest) int { return b.Number - a.Number })
	from := min(len(all), (page-1)*recentPullsPage)
	return slices.Clone(all[from:min(len(all), from+recentPullsPage)]), g.err
}

func (g *fakeGH) batchBot() string { return g.bot }

func (g *fakeGH) comments(int) ([]issueComment, error) {
	defer func() {
		if f := g.after["comments"]; f != nil {
			f()
		}
	}()
	return slices.Clone(g.issueComments), nil
}

func (g *fakeGH) createComment(number int, bodyFile string) error {
	if err := g.write(number, "a comment"); err != nil {
		return err
	}
	if err := g.inject("createComment"); err != nil {
		return err
	}
	g.commented = append(g.commented, number)
	defer func() {
		if f := g.after["createComment"]; f != nil {
			f()
		}
	}()
	data, err := os.ReadFile(bodyFile)
	var c issueComment
	c.ID, c.Body = int64(1000+len(g.issueComments)), string(data)
	c.User.Login, c.User.Type = "maintainer", "User"
	g.issueComments = append(g.issueComments, c)
	return err
}

func (g *fakeGH) editComment(id int64, bodyFile string) error {
	data, err := os.ReadFile(bodyFile)
	for i := range g.issueComments {
		if g.issueComments[i].ID == id {
			g.issueComments[i].Body = string(data)
			return err
		}
	}
	return fmt.Errorf("no comment %d", id)
}

func (g *fakeGH) labelled(label string) ([]int, error) {
	return g.labels[label], g.listErr
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
		if pr.Base.Ref == base && (pr.State == "" || pr.State == "open") {
			out = append(out, g.live(pr))
		}
	}
	return out, g.err
}

// createPull opens the pull request on the fake, as the maintainer, at the
// head the remote has, numbered nextPR.
func (g *fakeGH) createPull(base, head, title, bodyFile string) (int, string, error) {
	if err := g.inject("createPull"); err != nil {
		return 0, "", err
	}
	g.created = append(g.created, base+" "+head+" "+title)
	if err := g.readBody(bodyFile); err != nil {
		return 0, "", err
	}
	n := cmp.Or(g.nextPR, 90)
	var pr pullRequest
	pr.Number, pr.Body, pr.State = n, g.bodies[len(g.bodies)-1], "open"
	pr.HTMLURL = fmt.Sprintf("https://github.com/%s/pull/%d", upstreamRepo, n)
	pr.User.Login, pr.User.Type = "maintainer", "User"
	pr.Head.Ref = head
	if g.remoteHead != nil {
		pr.Head.SHA = g.remoteHead(head)
	}
	pr.Head.Repo = &struct {
		FullName string `json:"full_name"`
	}{upstreamRepo}
	pr.Base.Ref, pr.Base.Repo.FullName = base, upstreamRepo
	g.prs = append(slices.DeleteFunc(g.prs, func(p pullRequest) bool { return p.Number == n }), pr)
	return n, pr.HTMLURL, nil
}

func (g *fakeGH) editPull(number int, title, bodyFile string) error {
	if err := g.write(number, "an edit"); err != nil {
		return err
	}
	if err := g.inject("editPull"); err != nil {
		return err
	}
	g.edited = append(g.edited, fmt.Sprintf("#%d %s", number, title))
	if err := g.readBody(bodyFile); err != nil {
		return err
	}
	for i := range g.prs {
		if g.prs[i].Number == number {
			g.prs[i].Body = g.bodies[len(g.bodies)-1]
		}
	}
	return nil
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
	pr.State = "open"
	return pr
}

// botBatchPR is batchPR opened by the bot the scheduled batch runs as.
func (f *fixture) botBatchPR(gh *fakeGH, n int, base, branch, body string) pullRequest {
	f.t.Helper()
	gh.bot = defaultBatchBot
	pr := f.batchPR(n, base, branch, body)
	pr.User.Login, pr.User.Type, pr.AuthorAssociation = defaultBatchBot, "Bot", "CONTRIBUTOR"
	return pr
}

// mergeBatch merges the open batch into release-1.1 with a merge commit, as
// GitHub does, and deletes its branch, as the repository's
// delete_branch_on_merge does. The pull request stays, merged, with its body
// and comments.
func mergeBatch(f *fixture, gh *fakeGH) {
	f.t.Helper()
	f.git("fetch", "-q", "origin")
	f.git("switch", "-q", "--detach", "origin/release-1.1")
	f.git("merge", "-q", "--no-ff", "-m", "Merge pull request #90 from cozystack/"+batchBranch1, "origin/"+batchBranch1)
	f.git("push", "-q", "origin", "HEAD:refs/heads/release-1.1", ":refs/heads/"+batchBranch1)
	f.git("switch", "-q", "main")
	for i := range gh.prs {
		if gh.prs[i].Number == 90 {
			at := "2026-10-26T12:00:00Z"
			gh.prs[i].State, gh.prs[i].MergedAt = "closed", &at
		}
	}
}

// exec runs the tool in the fixture with gh faked, on the fixture's date, and
// fails the test when the tool wrote to a pull request that is not open.
func (f *fixture) exec(gh *fakeGH, args ...string) (int, string, string) {
	f.t.Helper()
	if gh == nil {
		gh = &fakeGH{}
	}
	if gh.remoteHead == nil {
		gh.remoteHead = f.remoteHead
	}
	var stdout, stderr bytes.Buffer
	a := &app{gh: gh, stdout: &stdout, stderr: &stderr, now: func() time.Time { return f.now }, tool: f.tool}
	written := len(gh.closedWrites)
	code := a.main(args)
	if len(gh.closedWrites) > written {
		f.t.Errorf("%v wrote to a pull request that is not open: %q", args, gh.closedWrites[written:])
	}
	return code, stdout.String(), stderr.String()
}

const batchBranch1 = "backport-batch/release-1.1-20261026"

// lines cuts and releases release-1.0 and then release-1.1 with one change
// between them, leaving release-1.1 the newer of the two lines the batch runs
// for.
func (f *fixture) lines() {
	f.t.Helper()
	f.release("release-1.0")
	f.tag("v1.0.0")
	f.pr(1, "feat(app): something for 1.1", "", map[string]string{"app/feature.txt": "1.1\n"})
	f.release("release-1.1")
	f.tag("v1.1.0")
}

// tag tags main's head on the remote: a stable release when the name has no
// pre-release suffix.
func (f *fixture) tag(name string) {
	f.t.Helper()
	f.git("push", "-q", "origin", "main:refs/tags/"+name)
}

// subjects lists the subjects of the units on branch beyond base, oldest
// first: the first-parent line, where a batch has one commit per original.
func (f *fixture) subjects(base, branch string) []string {
	f.t.Helper()
	out := f.git("log", "--reverse", "--first-parent", "--format=%s", base+".."+branch)
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// batchOnlyDay is the fixture's batch-only day: commits a test makes carry
// the clock's date, later than it, unless the test dates them.
const batchOnlyDay = "2026-10-26"

// policyWith is a policy page whose block lists entries, and the fixture's
// batch-only day unless entries give one.
func policyWith(entries string) string {
	if !strings.Contains(entries, batchOnlyKey+":") {
		entries += batchOnlyKey + ": " + batchOnlyDay + "\n"
	}
	return "## Backports\n\n" + policyFence + "\n# release-X.Y: YYYY-MM-DD\n" + entries + "```\n"
}

// setPolicy commits a policy page with entries onto main.
func (f *fixture) setPolicy(entries string) {
	f.t.Helper()
	f.squash(9000, "docs(release): set the policy", "", map[string]string{policyPage: policyWith(entries)})
}
