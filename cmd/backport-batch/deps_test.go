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
	"fmt"
	"slices"
	"strings"
	"testing"
)

// depsOf reads what original n builds on that the line lacks.
func depsOf(t testing.TB, f *fixture, n int) ([]dependency, []string) {
	t.Helper()
	f.git("fetch", "-q", "origin")
	b, err := shapeOf(line11)
	if err != nil {
		t.Fatal(err)
	}
	deps, diag, err := b.dependencies(b.find(n), b.lineHead)
	if err != nil {
		t.Fatal(err)
	}
	return deps, diag
}

// depNames lists dependencies as #N or a short commit, with their paths.
func depNames(deps []dependency) []string {
	var out []string
	for _, d := range deps {
		who := d.merge[:12]
		if d.v != nil {
			who = fmt.Sprintf("#%d", d.v.o.number)
		}
		out = append(out, fmt.Sprintf("%s %v %v", who, d.paths, d.added))
	}
	return out
}

// Every commit main gained since the line branched is brought by one commit
// of its first-parent line: a pull request's own commits by its merge, a
// direct push by itself, and a commit from before the fork point by none.
func TestBroughtBy(t *testing.T) {
	f := newFixture(t)
	f.lines()
	base := f.git("rev-parse", "origin/release-1.1")
	mc3 := f.prCommits(3, "feat(app): three", "",
		change{files: map[string]string{"app/3.txt": "3\n"}},
		change{mergeMain: true},
		change{files: map[string]string{"app/3.txt": "3b\n"}})
	f.write("app/push.txt", "pushed\n")
	f.git("add", "-A")
	f.git("commit", "-q", "-m", "chore: a direct push")
	f.git("push", "-q", "origin", "main")
	push := f.git("rev-parse", "HEAD")
	mc5 := f.pr(5, "fix(app): five", "", map[string]string{"app/five.txt": "5\n"})
	by, err := broughtBy(base, mc5)
	if err != nil {
		t.Fatal(err)
	}
	moved := f.git("rev-parse", mc3+"^1")
	for _, c := range strings.Fields(f.git("rev-list", mc3+"^1.."+mc3+"^2")) {
		if c == moved {
			continue
		}
		if by[c] != mc3 {
			t.Errorf("%.12s, a commit of #3's branch, is brought by %.12s, want #3's merge", c, by[c])
		}
	}
	for c, want := range map[string]string{mc3: mc3, moved: moved, push: push, mc5: mc5, f.git("rev-parse", mc5+"^2"): mc5} {
		if by[c] != want {
			t.Errorf("%.12s is brought by %.12s, want %.12s", c, by[c], want)
		}
	}
	if _, ok := by[base]; ok {
		t.Errorf("the fork point %.12s is in the map", base)
	}
}

// V1: a file the original changes that the line does not have names the
// first-parent commit on main that added it, a merge or a direct push.
func TestDependenciesV1(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(3, "feat(app): x", "", map[string]string{"app/x.txt": appFile})
	f.write("app/y.txt", appFile)
	f.git("add", "-A")
	f.git("commit", "-q", "-m", "chore: y, pushed")
	f.git("push", "-q", "origin", "main")
	push := f.git("rev-parse", "HEAD")
	f.pr(5, "fix(app): fix x and y", "", map[string]string{"app/x.txt": "fixed\n", "app/y.txt": "fixed\n"})
	deps, diag := depsOf(t, f, 5)
	want := []string{push[:12] + " [app/y.txt] true", "#3 [app/x.txt] true"}
	slices.Sort(want)
	got := depNames(deps)
	slices.Sort(got)
	if !slices.Equal(got, want) || len(diag) != 0 {
		t.Errorf("dependencies = %q, %q; want %q", got, diag, want)
	}
}

// v2World gives the line app/a.txt as the base had it, and main a feat that
// changes its second line and a fix of that same line.
func v2World(t testing.TB, onLine string) *fixture {
	f := newFixture(t)
	f.squash(2, "chore: a", "", map[string]string{"app/a.txt": appFile})
	f.lines()
	if onLine != "" {
		f.onLine("release-1.1", "fix(app): a, by hand", map[string]string{"app/a.txt": onLine})
	}
	f.pr(3, "feat(app): a loud two", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO", 1)})
	f.pr(5, "fix(app): an excited two", "", map[string]string{"app/a.txt": strings.Replace(appFile, "two", "TWO!", 1)})
	return f
}

// V2c: a line the original changes that blame gives to another original is
// a dependency only while the line's file lacks that line's text.
func TestDependenciesV2c(t *testing.T) {
	if deps, diag := depsOf(t, v2World(t, ""), 5); !slices.Equal(depNames(deps), []string{"#3 [app/a.txt] false"}) || len(diag) != 0 {
		t.Errorf("dependencies = %q, %q; want #3 through app/a.txt", depNames(deps), diag)
	}
	if deps, _ := depsOf(t, v2World(t, strings.Replace(appFile, "two", "TWO", 1)), 5); len(deps) != 0 {
		t.Errorf("dependencies with #3's line on the line by hand = %q; want none", depNames(deps))
	}
}

// The content check trims a line's edges and nothing else: a reindented line
// is there, a line with a space inside it is not.
func TestDependenciesTrimOnlyEdges(t *testing.T) {
	if deps, _ := depsOf(t, v2World(t, strings.Replace(appFile, "two", "  TWO\t", 1)), 5); len(deps) != 0 {
		t.Errorf("dependencies with #3's line reindented = %q; want none", depNames(deps))
	}
	if deps, _ := depsOf(t, v2World(t, strings.Replace(appFile, "two", "T WO", 1)), 5); !slices.Equal(depNames(deps), []string{"#3 [app/a.txt] false"}) {
		t.Errorf("dependencies with a space inside #3's line = %q; want #3", depNames(deps))
	}
}

// Lines a tool wrote are not blamed: a generated file whole, and the keys
// hack/update-crd.sh writes into an ApplicationDefinition, while the keys a
// person writes there are.
func TestDependenciesSkipGeneratedRanges(t *testing.T) {
	const rd = "packages/system/app-rd/cozyrds/app.yaml"
	rdFile := func(schema, category string) string {
		return "apiVersion: cozystack.io/v1alpha1\nkind: ApplicationDefinition\nmetadata:\n  name: app\nspec:\n  application:\n    kind: App\n    openAPISchema: |-\n      {\"v\":" + schema + "}\n  dashboard:\n    category: " + category + "\n    keysOrder: [[\"spec\", \"" + schema + "\"]]\n"
	}
	world := func(t *testing.T, before, feat, fix map[string]string) *fixture {
		f := newFixture(t)
		f.squash(2, "chore: generated files", "", before)
		f.lines()
		f.pr(3, "feat(api): regenerate", "", feat)
		f.pr(5, "fix(api): regenerate again", "", fix)
		return f
	}
	gen := "// Code generated by controller-gen. DO NOT EDIT.\n\npackage v1\n\nvar a = 1\n"
	f := world(t,
		map[string]string{"api/zz_generated.deepcopy.go": "package v1\n\nvar a = 1\n", "api/types_gen.go": gen, rd: rdFile("1", "A")},
		map[string]string{"api/zz_generated.deepcopy.go": "package v1\n\nvar a = 2\n", "api/types_gen.go": strings.Replace(gen, "= 1", "= 2", 1), rd: rdFile("2", "A")},
		map[string]string{"api/zz_generated.deepcopy.go": "package v1\n\nvar a = 3\n", "api/types_gen.go": strings.Replace(gen, "= 1", "= 3", 1), rd: rdFile("3", "A")})
	if deps, diag := depsOf(t, f, 5); len(deps) != 0 || len(diag) != 0 {
		t.Errorf("dependencies through generated lines = %q, %q; want none", depNames(deps), diag)
	}
	f = world(t, map[string]string{rd: rdFile("1", "A")}, map[string]string{rd: rdFile("2", "B")}, map[string]string{rd: rdFile("3", "C")})
	if deps, _ := depsOf(t, f, 5); !slices.Equal(depNames(deps), []string{"#3 [" + rd + "] false"}) {
		t.Errorf("dependencies through a key a person writes = %q; want #3", depNames(deps))
	}
}

// Evidence the detector cannot read, a binary file or a symlink, is a
// diagnostic and never a dependency.
func TestDependenciesIncompleteNeverParks(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: an image", "", map[string]string{"app/a.bin": "a\x00one"})
	f.lines()
	f.pr(3, "feat(app): a new image", "", map[string]string{"app/a.bin": "a\x00two"})
	f.pr(5, "fix(app): a fixed image", "", map[string]string{"app/a.bin": "a\x00three"})
	if deps, diag := depsOf(t, f, 5); len(deps) != 0 || !slices.Equal(diag, []string{`incomplete evidence: "app/a.bin" is binary`}) {
		t.Errorf("dependencies = %q, %q; want none and a diagnostic", depNames(deps), diag)
	}
}

// Output of git the detector cannot read whole is an error, never taken
// for an absent dependency.
func TestDependenciesMalformedOutputIsAnError(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for name, out := range map[string]string{
		"a short header":        sha + " 1\n\tline\n",
		"a header of no commit": "aaaa 1 1 1\n\tline\n",
		"a header of no number": sha + " x 1 1\n\tline\n",
		"a line with no header": "\tline\n",
		"a header with no line": sha + " 1 1 1\nauthor x\n",
	} {
		if _, err := parseBlame(out); err == nil {
			t.Errorf("%s: parseBlame accepted %q", name, out)
		}
	}
	if lines, err := parseBlame(sha + " 2 2 1\nauthor x\nfilename a\n\ttwo\n" + sha + " 4 4\n\tfour\n"); err != nil || len(lines) != 2 || lines[1].text != "four" {
		t.Errorf("parseBlame of sound output = %+v, %v", lines, err)
	}
	if _, _, err := oldRanges("@@ -x +1 @@\n"); err == nil {
		t.Error("oldRanges accepted an unreadable hunk header")
	}
}

// Paths go to git as they are: a name a pathspec would read as a pattern
// means only itself.
func TestDependenciesLiteralPaths(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(3, "feat(app): a bracketed file", "", map[string]string{"app/[x].txt": appFile})
	f.pr(4, "feat(app): x", "", map[string]string{"app/x.txt": appFile})
	f.pr(5, "fix(app): fix the bracketed file", "", map[string]string{"app/[x].txt": "fixed\n"})
	if deps, _ := depsOf(t, f, 5); !slices.Equal(depNames(deps), []string{"#3 [app/[x].txt] true"}) {
		t.Errorf("dependencies = %q; want #3 alone", depNames(deps))
	}
}

// A file with no final newline blames its last line like any other.
func TestDependenciesNoFinalNewline(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: a", "", map[string]string{"app/a.txt": "one\ntwo"})
	f.lines()
	f.pr(3, "feat(app): a loud two", "", map[string]string{"app/a.txt": "one\nTWO"})
	f.pr(5, "fix(app): an excited two", "", map[string]string{"app/a.txt": "one\nTWO!"})
	if deps, diag := depsOf(t, f, 5); !slices.Equal(depNames(deps), []string{"#3 [app/a.txt] false"}) || len(diag) != 0 {
		t.Errorf("dependencies = %q, %q; want #3", depNames(deps), diag)
	}
}

// Blame takes the revision as a full commit name, which git cannot read as
// an option, and refuses --end-of-options, so the tool passes none.
func TestBlameInvocation(t *testing.T) {
	sha := strings.Repeat("a", 40)
	args, err := blameArgs(sha, "-x", []hunk{{2, 3}, {9, 1}})
	if want := []string{"blame", "--porcelain", "-w", "-L", "2,+3", "-L", "9,+1", sha, "--", "-x"}; err != nil || !slices.Equal(args, want) {
		t.Errorf("blameArgs = %q, %v; want %q", args, err, want)
	}
	for _, rev := range []string{"HEAD", "--output=x", strings.Repeat("a", 39)} {
		if _, err := blameArgs(rev, "a", nil); err == nil {
			t.Errorf("blameArgs took %q for a revision", rev)
		}
	}
}

// What the detector could not read shows with the original whatever became
// of it: picked, waiting for a maintainer, or a deferred conflict, in the
// body and in the report.
func TestDiagnosticsShowOnEveryOutcome(t *testing.T) {
	f := newFixture(t)
	f.squash(2, "chore: images", "", map[string]string{"app/p.bin": "p\x00one", "app/w.bin": "w\x00one", "app/c.bin": "c\x00one", "app/c.txt": "old\n"})
	f.lines()
	f.onLine("release-1.1", "fix(app): c, on the line", map[string]string{"app/c.txt": "line\n"})
	f.pr(5, "fix(app): picked", "", map[string]string{"app/p.bin": "p\x00two"})
	f.pr(6, "fix(ci): waiting", "", map[string]string{"app/w.bin": "w\x00two", ".github/workflows/six.yaml": "on: push\n"})
	f.pr(7, "fix(app): conflicting", "", map[string]string{"app/c.bin": "c\x00two", "app/c.txt": "seven\n"})
	code, stdout, stderr := f.exec(nil, "run", "--line", "release-1.1", "--no-workflow-files")
	if code != 0 {
		t.Fatalf("run = %d\n%s", code, stderr)
	}
	for _, s := range []struct{ section, file string }{{"[#5]", "p.bin"}, {"### Deferred conflicts", "c.bin"}, {"### Waiting for a maintainer", "w.bin"}} {
		_, after, ok := strings.Cut(stdout, s.section)
		if want := "  - `incomplete evidence: \"app/" + s.file + "\" is binary`"; !ok || !strings.Contains(after, want) {
			t.Errorf("the body does not show %q after %s:\n%s", want, s.section, stdout)
		}
	}
	for n, file := range map[int]string{5: "p.bin", 6: "w.bin", 7: "c.bin"} {
		if want := fmt.Sprintf(`"%d":["incomplete evidence: \"app/%s\" is binary"]`, n, file); !strings.Contains(stdout, want) {
			t.Errorf("the report lacks %s:\n%s", want, stdout)
		}
	}
}

// BenchmarkAnalyze reads what forty originals build on, over a history
// where half of them build on a feat the line lacks.
func BenchmarkAnalyze(b *testing.B) {
	f := newFixture(b)
	f.squash(2, "chore: files", "", map[string]string{"app/a.txt": appFile})
	f.lines()
	for n := 10; n < 50; n++ {
		file := fmt.Sprintf("app/f%d.txt", n)
		if n%2 == 0 {
			f.pr(n, "feat(app): "+file, "", map[string]string{file: appFile})
			continue
		}
		prev := fmt.Sprintf("app/f%d.txt", n-1)
		f.pr(n, "fix(app): "+prev, "", map[string]string{prev: strings.Replace(appFile, "two", fmt.Sprint(n), 1), "app/a.txt": strings.Replace(appFile, "three", fmt.Sprint(n), 1)})
	}
	f.git("fetch", "-q", "origin")
	b.ResetTimer()
	for range b.N {
		bt, err := shapeOf(line11)
		if err != nil {
			b.Fatal(err)
		}
		if err := bt.analyze(); err != nil {
			b.Fatal(err)
		}
	}
}

// Files the line lacks count toward the files an original may have read
// for it, like the ones blamed.
func TestDependenciesCapMissingFiles(t *testing.T) {
	f := newFixture(t)
	f.lines()
	added, fixed := map[string]string{}, map[string]string{}
	for i := range maxDepFiles + 1 {
		added[fmt.Sprintf("app/f%03d.txt", i)] = "v1\n"
		fixed[fmt.Sprintf("app/f%03d.txt", i)] = "v2\n"
	}
	f.pr(3, "feat(app): many files", "", added)
	f.pr(5, "fix(app): all of them", "", fixed)
	if _, diag := depsOf(t, f, 5); !slices.ContainsFunc(diag, func(d string) bool { return strings.Contains(d, fmt.Sprintf("more than %d files", maxDepFiles)) }) {
		t.Errorf("diag = %q, want the file cap", diag)
	}
}
