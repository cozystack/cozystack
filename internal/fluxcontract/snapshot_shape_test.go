// Package fluxcontract holds contract tests for upstream Flux types this
// repository reads through kubectl rather than through the Go API.
//
// A shell script that pulls a field out of a Kubernetes object with
// `kubectl -o jsonpath` depends on the serialized shape of an upstream Go
// struct, and nothing in the shell layer can observe that dependency. Rename
// the field upstream and the expression stops matching: kubectl prints
// nothing, exits zero, and the caller reads the empty output as an answer
// about the object rather than as a question that no longer parses.
//
// The test for that cannot live in the shell layer either. Pointing an
// expression at a document written by the same test proves the reader can read
// that document and nothing else, because a rename moves neither side.
//
// So this file supplies neither half. The expression comes from the shell
// library that ships it, obtained by sourcing that library, and the object is
// built from the upstream type at the version go.mod pins. What is left to
// assert is that the one still finds the other.
//
// One test does that. The rest exist because the expression does not travel to
// kubectl directly: it goes through a shell variable that one library assigns
// and the caller expands, and every step of that indirection can fail without
// saying so, including the way this file resolves the value in the first place.
package fluxcontract

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/util/jsonpath"
	"sigs.k8s.io/yaml"
)

const (
	// The shell library that owns the expression, and the script that runs it.
	guardLibrary = "../../hack/e2e-chainsaw/_lib/remediation-guard.sh"
	guardCaller  = "../../hack/e2e-chainsaw/_lib/run-kubernetes.sh"

	// The Chainsaw suites, one directory each, which may call the guard from a
	// script step.
	chainsawSuites = "../../hack/e2e-chainsaw"

	sharedName = "HELMRELEASE_HISTORY_JSONPATH"
)

// The name followed by an `=`, wherever it appears and however indented. Named
// after what it matches rather than what it is used to find: an occurrence is
// not an assignment, a comment mentioning one counts, and a message built on
// this has to say occurrence or it claims more than the pattern saw.
//
// Derived from the const rather than spelled out, so that a rename carries the
// pattern with it. A literal here survives the rename as a search for a name
// nothing assigns any more, which finds nothing and reads as a pass, while the
// failure message goes on interpolating the const and naming the new one. The
// refuse case below still spells the name out, so a rename that reaches only
// half the file arrives as that case going red rather than as a pin that
// quietly stopped looking.
var nameFollowedByEquals = regexp.MustCompile(regexp.QuoteMeta(sharedName) + `=`)

// The function the guard's result comes from, and the local handed to it.
//
// The read is found through this call rather than by looking for a well-formed
// read anywhere in the file. Those are different assertions: the second is
// satisfied by any read that takes the shared expansion, including one the
// guard never consumes, so a second assignment overwriting the consumed local
// with a literal jsonpath leaves the pin green over exactly the unpinned
// expression it exists to forbid. Anchoring on the call names the one read
// whose expression reaches upstream.
//
// The `if` prefix, the negation, and the quoting of the argument are left
// free, because this script mixes forms that behave identically. Each is a
// case in the accept table below.
//
// The reach is one named caller, the Chainsaw suites, and calls that pass a
// local. Any other script that reads history for the guard, or a caller passing
// a substitution written out in the argument, is outside what this sees. Neither
// exists today (the bats tests hand the guard literal input and read no
// history), and the rule for authors is prose rather than a pattern:
// docs/agents/e2e-testing.md tells them to take the expression from the shared
// name.
var guardConsumers = regexp.MustCompile(
	`(?m)^[ \t]*(?:if[ \t]+)?(?:![ \t]*)?helmrelease_has_remediation_cycle[ \t]+"?\$\{?([A-Za-z_][A-Za-z0-9_]*)`)

// sharedReadBy builds the read that fills one named local: a line-starting
// assignment to that exact name, taking its value from a kubectl call that
// passes the expansion of the shared name as its jsonpath.
//
// The `$` is load-bearing rather than decorative. Matching the bare name would
// accept `jsonpath=HELMRELEASE_HISTORY_JSONPATH`, which kubectl reads as a
// template with no braces and echoes back verbatim. That output is not empty,
// so the script's own empty-history backstop stays quiet while carrying no
// status the cycle check can match, which is this file's whole subject
// arriving with the pin green over it.
//
// The spelling of `-o` and the quoting of the substitution are left free, for
// the reason above; each is a case in the accept table below, so that "free"
// is enforced rather than announced. A spelling absent from that table is one
// nobody checked, not one known to be rejected.
//
// A form this pattern does not cover reddens rather than passing: the caller
// test fails with the pattern printed. So the list is a record of what was
// checked and not a contract, and a rewrite outside it costs a red in the same
// run rather than a pin that quietly stops asserting. That is why `readonly`,
// `export` and `local` prefixes are not accepted here and need no case: each
// fails loudly and visibly. `local` would not survive sourcing anyway, and
// `declare` is not portable to the dash and busybox shells this runs under.
//
// The quoting of the expansion is not among them, and is not pinned either.
// Quoting the substitution wraps its result; the expansion inside still has to
// carry its own quotes, or the space in `{range .status.history[*]}` splits
// the argument and kubectl is handed a truncated template. That is left
// unpinned because it fails loudly: kubectl rejects the fragment, the variable
// comes back empty, and the script's empty-history check exits 1. The `$` is
// pinned because dropping it fails quietly instead, returning the name as its
// own text and passing every check downstream. What earns a pin here is the
// silence of the failure, not its severity.
//
// An accept case is a form a maintainer may rewrite the read into, so a case
// that changes behaviour would document a broken rewrite as legitimate.
// The trailing `\b` ends the name. Without it a read left pointing at a retired
// `..._OLD` or `..._V1` beside the shared name matches on the prefix and
// satisfies the pin while expanding to something else entirely. Both spellings
// leak that way, not the braceless one alone: the `{` here is optional and no
// closing `}` is required after the name, so nothing bounds it but this. Both
// are in the refuse table, because covering only the spelling that looks
// unterminated is how a later rewrite reopens the other one.
func sharedReadBy(local string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(local) +
		`="?\$\(kubectl[^)]*jsonpath=[^)]*\$\{?` + sharedName + `\b`)
}

// localFollowedByEquals counts occurrences of one named local followed by an
// `=`. One read matching the shared form says nothing about a second assignment
// to the same name: the shell takes the last one, the pattern takes any one,
// and the two need not be the same statement. Requiring a single occurrence is
// what makes the matched read the read that runs.
//
// Named after what it matches, like the sibling pattern above and for the same
// reason: an occurrence is not an assignment. Deliberately not anchored to the
// start of a line. An anchor here would have to enumerate the ways an
// assignment can begin somewhere other than column one, and each spelling it
// missed would be a second assignment counted as none: `export`, `readonly` and
// anything after a `;` all run, and all leave the count at one. That is
// reimplementing enough of the shell to decide what an assignment is, which
// this file refuses to do elsewhere for the same reason. Counting occurrences
// instead errs toward two where there is one, and a miscount in that direction
// reddens.
//
// The leading `\b` keeps a longer name ending in this one, `prev_history` for
// `history`, from being counted as it.
func localFollowedByEquals(local string) *regexp.Regexp {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(local) + `=`)
}

func readGuardFile(t *testing.T, path string) []byte {
	t.Helper()
	src, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return src
}

// The snippet that resolves the library's value. The path goes in as an
// argument rather than into the script text, so no quoting of it can change
// what the snippet does. `|| exit 1` is what makes a broken library reach the
// error branch at all: without it the trailing printf is the last command, so a
// library that fails to source still exits 0 and the failure arrives only as a
// value that happens to look wrong.
//
// The leading `unset` is what keeps the answer coming from the library rather
// than from whoever ran the test. `go test` hands the child its own
// environment, so a shell that exported this name has already answered the
// question the snippet asks: a library that stopped assigning it would print
// the inherited value, the empty check below would not fire, and the pin would
// go on passing over a guard that reads nothing. That is the silent retirement
// this file refuses elsewhere, arriving through the environment instead of
// through the source.
const sourceAndPrint = `unset ` + sharedName + `; . "$1" || exit 1; printf %s "${` + sharedName + `-}"`

// sourcedExpression returns the value the shell ends up with after sourcing the
// guard library, which is the value the guard passes to kubectl.
//
// This runs `sh` from a Go test, which is unusual here and deliberate. Reading
// the assignment as text means reimplementing enough of the shell to decide
// which text is the assignment, and every such approximation is wrong at some
// spelling: an `export`, an indent inside a function, a second assignment later
// in the file. Sourcing gets last-assignment-wins, `export`, `readonly` and
// top-level indentation for free, because the thing resolving them is the shell
// rather than a guess about it. What it does not get is an assignment inside a
// function: sourcing defines the function without calling it, so the value
// comes back empty and the empty check below fails, which is the right answer
// for a library that no longer sets the variable it exports.
//
// A missing `sh` fails rather than skips. A skip prints in verbose output and
// reads as a pass in the summary, so an environment without a shell would
// retire this pin silently, which is the failure mode the whole file exists to
// prevent. Anything that cannot run `sh` cannot run the script being pinned.
func sourcedExpression(t *testing.T) string {
	t.Helper()

	if _, err := exec.LookPath("sh"); err != nil {
		t.Fatalf("sh not found (%v), so the guard's own expression cannot be resolved. "+
			"This fails rather than skips: a skipped pin reads as a passing one.", err)
	}

	out, err := exec.Command("sh", "-c", sourceAndPrint, "sh", guardLibrary).Output()
	if err != nil {
		// Output() puts the shell's own complaint in ExitError.Stderr, and %v on
		// that error prints only the exit status. This path runs exactly when
		// something is wrong, so dropping the one sentence that says what would
		// leave the reader with the class and not the cause.
		detail := ""
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			detail = ": " + strings.TrimSpace(string(ee.Stderr))
		}
		t.Fatalf("sourcing %s: %v%s. The library has to be sourceable on its own, since the "+
			"e2e script sources it the same way.", guardLibrary, err, detail)
	}

	expr := string(out)
	if expr == "" {
		t.Fatalf("sourcing %s leaves %s empty. If the guard reads release history another way "+
			"now, this test has to follow it there. If it stopped reading history at all, the "+
			"e2e remediation guard asserts nothing.", guardLibrary, sharedName)
	}
	return expr
}

// The carrier for sourceAndPrint. It runs the snippet against libraries written
// here rather than against the shipped one, because what is under test is the
// snippet itself and not anything about the guard: a fixture is the right
// instrument for that and the wrong one for the contract next door.
//
// The snippet is one line and every piece of it fails green when removed, so
// each piece gets a case and no case covers for another. Sourcing: without it
// the value comes back empty rather than as the library's, which the second
// case, wanting empty, reads as success. Unsetting: without it the inherited
// value answers in place of a library that assigns nothing, which the first
// case, whose library does assign, never sees. Exiting on a failed source:
// without it a library that assigns the name and then fails still prints the
// value and returns 0, which both other cases read as an ordinary success.
func TestSourcingTakesTheLibrarysValueNotAnInheritedOne(t *testing.T) {
	const inherited = "{.inherited}"

	write := func(t *testing.T, body string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "lib.sh")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("writing the throwaway library: %v", err)
		}
		return path
	}

	// Returns what the snippet printed and whether it failed, because one case
	// is about the failure and the others are about the value.
	run := func(t *testing.T, path string) (string, error) {
		t.Helper()
		cmd := exec.Command("sh", "-c", sourceAndPrint, "sh", path)
		cmd.Env = append(os.Environ(), sharedName+"="+inherited)
		out, err := cmd.Output()
		return string(out), err
	}

	mustRun := func(t *testing.T, path string) string {
		t.Helper()
		out, err := run(t, path)
		if err != nil {
			t.Fatalf("running the snippet: %v", err)
		}
		return out
	}

	t.Run("a library that assigns the name wins over the environment", func(t *testing.T) {
		got := mustRun(t, write(t, sharedName+"='{.fromLibrary}'\n"))
		if got != "{.fromLibrary}" {
			t.Errorf("got %q, want the library's value; an inherited value must not reach the caller", got)
		}
	})

	t.Run("a library that stops assigning the name comes back empty", func(t *testing.T) {
		if got := mustRun(t, write(t, "# this library no longer assigns it\n")); got != "" {
			t.Errorf("got %q, want empty. A value here is the environment answering for the "+
				"library, which retires the pin without failing it.", got)
		}
	})

	// The assignment comes before the failure deliberately. A library that
	// breaks before assigning is caught by the empty value; one that breaks
	// after has already supplied a value that looks entirely correct, so the
	// exit status is the only thing left that knows the library is broken.
	t.Run("a library that fails after assigning the name fails the snippet", func(t *testing.T) {
		path := write(t, sharedName+"='{.fromLibrary}'\nthis_command_does_not_exist\n")
		out, err := run(t, path)
		if err == nil {
			t.Errorf("the snippet returned 0 and printed %q for a library that failed to source. "+
				"Sourcing has to fail the snippet, or a library that half-ran is indistinguishable "+
				"from one that ran.", out)
		}
	})
}

// A HelmRelease whose history carries the statuses the guard reads. Built from
// the upstream types, so a renamed or retyped field is a compile error here
// before it is a silent empty read against a live cluster.
func helmReleaseWithHistory() *helmv2.HelmRelease {
	return &helmv2.HelmRelease{
		Status: helmv2.HelmReleaseStatus{
			History: helmv2.Snapshots{
				{Version: 2, Status: "deployed"},
				{Version: 1, Status: "uninstalled"},
			},
		},
	}
}

// The only test here whose subject is upstream rather than the text of a shell
// script: the expression the guard ships, run over the upstream type through
// JSON the way kubectl runs it. Serialization is the point, since the struct's
// json tags are what an expression actually sees.
//
// Only the match is asserted. How kubectl's printer behaves on a path that is
// absent is kubectl's own configuration, not part of the Flux contract, and
// guessing at it here would put the expectation and the fixture back in the
// same hands.
func TestGuardExpressionReadsUpstreamHistoryStatuses(t *testing.T) {
	raw, err := json.Marshal(helmReleaseWithHistory())
	if err != nil {
		t.Fatalf("marshalling HelmRelease: %v", err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshalling HelmRelease: %v", err)
	}

	expr := sourcedExpression(t)

	jp := jsonpath.New("guard")
	if err := jp.Parse(expr); err != nil {
		t.Fatalf("parsing the guard's expression %q: %v", expr, err)
	}
	var out strings.Builder
	if err := jp.Execute(&out, generic); err != nil {
		t.Fatalf("running the guard's expression %q over an upstream HelmRelease: %v. "+
			"The serialized shape no longer satisfies the expression.", expr, err)
	}

	var got []string
	for line := range strings.SplitSeq(out.String(), "\n") {
		if line != "" {
			got = append(got, line)
		}
	}
	want := []string{"deployed", "uninstalled"}
	if len(got) != len(want) {
		t.Fatalf("expression %q returned %q, want one line per Snapshot status %q",
			expr, out.String(), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("status %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// The value the caller hands the guard comes from a read of the expansion of
// the shared name. Naming it without expanding it, naming it only in a comment
// beside a rewritten read, or filling the consumed local somewhere else leaves
// the guard running an expression nothing here checked.
func TestGuardCallerReferencesTheSharedExpansion(t *testing.T) {
	t.Run("the call", func(t *testing.T) {
		for _, tc := range []struct{ spelling, line, local string }{
			{"shipped form", `  if helmrelease_has_remediation_cycle "${history_statuses}"; then`, "history_statuses"},
			{"negated", `  if ! helmrelease_has_remediation_cycle "${history_statuses}"; then`, "history_statuses"},
			{"bare call", `  helmrelease_has_remediation_cycle "${hr_history}"`, "hr_history"},
			{"braceless expansion", `  if helmrelease_has_remediation_cycle "$history_statuses"; then`, "history_statuses"},
		} {
			t.Run(tc.spelling, func(t *testing.T) {
				m := guardConsumers.FindStringSubmatch(tc.line)
				if m == nil {
					t.Fatalf("a legitimate call was not taken as one: %s", tc.line)
				}
				if m[1] != tc.local {
					t.Errorf("read the consumed local as %q, want %q, from: %s", m[1], tc.local, tc.line)
				}
			})
		}

		t.Run("refuses a commented-out call", func(t *testing.T) {
			line := `  # if helmrelease_has_remediation_cycle "${history_statuses}"; then`
			if guardConsumers.MatchString(line) {
				t.Errorf("taken as a call to the guard: %s", line)
			}
		})
	})

	t.Run("the read", func(t *testing.T) {
		t.Run("refuses", func(t *testing.T) {
			for _, tc := range []struct{ flaw, local, line string }{
				{"name not expanded", "h", `  h=$(kubectl get hr -o"jsonpath=HELMRELEASE_HISTORY_JSONPATH")`},
				{"name only in a trailing comment", "h", `  h=$(kubectl get hr -o"jsonpath={.status.gone}") # was ${HELMRELEASE_HISTORY_JSONPATH}`},
				{"the read is commented out", "history_statuses", `  # history_statuses=$(kubectl get hr -o"jsonpath=${HELMRELEASE_HISTORY_JSONPATH}")`},
				{"another local carries the shared read", "history_statuses", `  example=$(kubectl get hr -o"jsonpath=${HELMRELEASE_HISTORY_JSONPATH}")`},
				{"a longer name starting with the shared one", "history_statuses", `  history_statuses=$(kubectl get hr -o"jsonpath=$HELMRELEASE_HISTORY_JSONPATH_OLD")`},
				{"the same, braced", "history_statuses", `  history_statuses=$(kubectl get hr -o"jsonpath=${HELMRELEASE_HISTORY_JSONPATH_OLD}")`},
			} {
				t.Run(tc.flaw, func(t *testing.T) {
					if sharedReadBy(tc.local).MatchString(tc.line) {
						t.Errorf("taken as the read filling %s: %s", tc.local, tc.line)
					}
				})
			}
		})

		// A refuse case has to be a form the shell would not run as the read,
		// the mirror of the rule for accept cases. A legitimate rewrite listed
		// there would pin the pattern's narrowness as though it were the intent.
		t.Run("accepts", func(t *testing.T) {
			for _, tc := range []struct{ spelling, local, line string }{
				{"shipped form", "history_statuses", `  history_statuses=$(kubectl get hr -o"jsonpath=${HELMRELEASE_HISTORY_JSONPATH}")`},
				{"quoted substitution", "history_statuses", `  history_statuses="$(kubectl get hr -ojsonpath="${HELMRELEASE_HISTORY_JSONPATH}")"`},
				{"local renamed", "hr_history", `  hr_history=$(kubectl get hr -o"jsonpath=${HELMRELEASE_HISTORY_JSONPATH}")`},
				{"spaced -o", "history_statuses", `  history_statuses=$(kubectl get hr -o jsonpath="${HELMRELEASE_HISTORY_JSONPATH}")`},
				{"braceless expansion", "history_statuses", `  history_statuses=$(kubectl get hr -o"jsonpath=$HELMRELEASE_HISTORY_JSONPATH")`},
			} {
				t.Run(tc.spelling, func(t *testing.T) {
					if !sharedReadBy(tc.local).MatchString(tc.line) {
						t.Errorf("a legitimate read was not taken as the read filling %s: %s", tc.local, tc.line)
					}
				})
			}
		})

		// The case the shared-read pattern cannot answer on its own: the shipped
		// read, then a second one filling the same local with a literal the
		// shell hands the guard. Matching a read says the file contains one;
		// counting occurrences is what says the one it contains is the one that
		// runs.
		//
		// One spelling per way an assignment can begin off column one, because
		// an anchored count sees none of them and reports the single occurrence
		// it was going to report anyway.
		t.Run("a second assignment is not hidden by the first", func(t *testing.T) {
			const shipped = "  history_statuses=$(kubectl get hr -o\"jsonpath=${HELMRELEASE_HISTORY_JSONPATH}\")\n"
			for _, tc := range []struct{ spelling, second string }{
				{"on its own line", "  history_statuses=$(kubectl get hr -o\"jsonpath={.statusz}\")\n"},
				{"export prefix", "  export history_statuses=$(kubectl get hr -o\"jsonpath={.statusz}\")\n"},
				{"readonly prefix", "  readonly history_statuses=$(kubectl get hr -o\"jsonpath={.statusz}\")\n"},
				{"after a semicolon", "  : ; history_statuses=$(kubectl get hr -o\"jsonpath={.statusz}\")\n"},
			} {
				t.Run(tc.spelling, func(t *testing.T) {
					overwritten := []byte(shipped + tc.second)
					if !sharedReadBy("history_statuses").Match(overwritten) {
						t.Fatal("the shipped form stopped matching, so this case no longer shows what it was written for")
					}
					if n := len(localFollowedByEquals("history_statuses").FindAll(overwritten, -1)); n != 2 {
						t.Errorf("counted %d occurrences of the consumed local followed by =, want 2", n)
					}
				})
			}

			t.Run("a longer name ending in the consumed one is not counted", func(t *testing.T) {
				line := []byte("  prev_history_statuses=$(kubectl get hr -o\"jsonpath={.statusz}\")\n")
				if n := len(localFollowedByEquals("history_statuses").FindAll(line, -1)); n != 0 {
					t.Errorf("counted %d occurrences, want 0: a different local must not read as this one", n)
				}
			})
		})
	})

	src := readGuardFile(t, guardCaller)

	consumers := guardConsumers.FindAllSubmatchIndex(src, -1)
	if len(consumers) == 0 {
		t.Fatalf("%s never calls helmrelease_has_remediation_cycle with an expanded local, so there "+
			"is no read for this pin to follow. Either the caller stopped running the guard, or it "+
			"calls it in a shape %s cannot see.", guardCaller, guardConsumers)
	}

	assertConsumedLocalsTakeTheSharedRead(t, guardCaller, src, consumers)
}

func assertConsumedLocalsTakeTheSharedRead(t testing.TB, path string, src []byte, consumers [][]int) {
	t.Helper()
	for _, m := range consumers {
		local := string(src[m[2]:m[3]])
		n := len(localFollowedByEquals(local).FindAll(src, -1))
		if n != 1 {
			t.Errorf("%s writes %s, the local it hands the guard, followed by = %d times; want "+
				"exactly one. The count is of occurrences, so a comment mentioning one counts too; "+
				"what it looks for is a second assignment, which the shell would take instead of "+
				"the one this pin matched, putting an unpinned expression in front of the guard.",
				path, local, n)
			continue
		}
		if !sharedReadBy(local).Match(src) {
			t.Errorf("%s fills %s, the local it hands the guard, in a shape that does not take its "+
				"expression from %s. The pattern is the exact form accepted: %s. Either the read "+
				"stopped using the shared assignment, or it uses it in a shape the pattern cannot "+
				"see.", path, local, sharedName, sharedReadBy(local))
			continue
		}
		if !sharedReadPrecedes(src, m) {
			t.Errorf("%s fills %s from %s only after handing it to the guard, so the guard runs "+
				"on whatever the local held before the read.", path, local, sharedName)
		}
	}
}

// sharedReadPrecedes reports whether the shared read of the local consumed by
// the guard call m starts before that call. m holds the call's offsets as
// FindAllSubmatchIndex returns them, so the call is placed where it matched and
// not wherever its text first appears, a comment included. Text order stands
// in for run order, which holds for a read and a call in one straight-line
// block, as in every caller this file reads today.
func sharedReadPrecedes(src []byte, m []int) bool {
	read := sharedReadBy(string(src[m[2]:m[3]])).FindIndex(src)
	return read != nil && read[0] < m[0]
}

// A Chainsaw suite that sources the guard library from a script step is a
// caller too, and one this file would otherwise never read: the pin above names
// one script, so a suite writing the jsonpath out as a literal runs an
// expression nothing here checked.
//
// Every suite is read rather than a named one, because the next suite to adopt
// the guard is the likeliest place for a copied literal and naming suites here
// would need the same edit that introduces it. A suite cannot assign the shared
// name either: its script sources the library first, so its own assignment
// lands afterwards and replaces the value the upstream test resolved.
//
// Both sets are asserted non-empty. A glob that stops matching, or suites that
// all stop calling the guard, leave the loop with nothing to check, and that
// reads exactly like a pass.
func TestChainsawSuitesReadHistoryThroughTheSharedName(t *testing.T) {
	var suites []string
	for _, pattern := range []string{"chainsaw-test.yaml", "chainsaw-test.yml"} {
		found, err := filepath.Glob(filepath.Join(chainsawSuites, "*", pattern))
		if err != nil {
			t.Fatalf("globbing the Chainsaw suites: %v", err)
		}
		suites = append(suites, found...)
	}
	if len(suites) == 0 {
		t.Fatalf("no Chainsaw suites found under %s, so this test asserted nothing", chainsawSuites)
	}

	// Each script step runs in a shell of its own, so the read the guard sees is
	// the one in its own step. Checked against the whole file, a read in one
	// step would satisfy a call in another, and an unrelated assignment of the
	// same local elsewhere would fail a correct one.
	t.Run("a read in another step does not count", func(t *testing.T) {
		steps := scriptSteps(t, "fixture", []byte(`spec:
  steps:
  - try:
    - script:
        content: |
          statuses=$(kubectl get hr -o "jsonpath=${HELMRELEASE_HISTORY_JSONPATH}")
--- # a separator may carry a comment
spec:
  steps:
  - try:
    - script:
        content: |
          helmrelease_has_remediation_cycle "$statuses"
`))
		if len(steps) != 2 {
			t.Fatalf("split the fixture into %d script steps, want 2", len(steps))
		}
		rec := &errorRecorder{TB: t}
		if n := checkSuiteSteps(rec, "fixture", steps); n != 1 {
			t.Fatalf("counted %d guard callers in the fixture, want 1", n)
		}
		if len(rec.errs) == 0 {
			t.Error("a guard call whose only read sits in another step passed the check")
		}
	})

	// The read has to come before the call: one after it fills the local only
	// once the guard has already run, on an empty or unset value.
	t.Run("a read after the guard call does not count", func(t *testing.T) {
		const read = "statuses=$(kubectl get hr -o \"jsonpath=${HELMRELEASE_HISTORY_JSONPATH}\")\n"
		const call = "helmrelease_has_remediation_cycle \"$statuses\"\n"
		for _, tc := range []struct {
			name, step string
			want       bool
		}{
			{"read first", read + call, true},
			{"call first", call + read, false},
			// The call's own text in a comment above the read is not the call.
			{"call text in a comment first", "# then " + call + read + call, true},
		} {
			ms := guardConsumers.FindAllSubmatchIndex([]byte(tc.step), -1)
			if len(ms) != 1 {
				t.Fatalf("%s: found %d guard calls, want 1", tc.name, len(ms))
			}
			m := ms[0]
			if got := sharedReadPrecedes([]byte(tc.step), m); got != tc.want {
				t.Errorf("%s: sharedReadPrecedes = %v, want %v", tc.name, got, tc.want)
			}
			// The same step through the suite check, so the order test is
			// one the check runs and not only a property of the helper.
			rec := &errorRecorder{TB: t}
			checkSuiteSteps(rec, "fixture", []string{tc.step})
			if refused := len(rec.errs) > 0; refused == tc.want {
				t.Errorf("%s: the suite check refused the step = %v, want %v: %v", tc.name, refused, !tc.want, rec.errs)
			}
		}
	})

	callers := 0
	for _, path := range suites {
		src := readGuardFile(t, path)
		if n := len(nameFollowedByEquals.FindAll(src, -1)); n != 0 {
			t.Errorf("%s writes %s followed by = %d times; only %s may assign it. A suite sources "+
				"the library before its own lines run, so its assignment replaces the value the "+
				"upstream test checked.", path, sharedName, n, guardLibrary)
		}
		callers += checkSuiteSteps(t, path, scriptSteps(t, path, src))
	}
	if callers == 0 {
		t.Fatalf("no Chainsaw suite under %s calls helmrelease_has_remediation_cycle with an "+
			"expanded local, so this test asserted nothing. If no suite runs the guard any more, "+
			"this test goes with it.", chainsawSuites)
	}
}

// The content of every script step in a Chainsaw file, wherever it sits: try,
// catch, finally, or a nested operation.
func scriptSteps(t testing.TB, path string, src []byte) []string {
	t.Helper()
	var steps []string
	var walk func(any)
	walk = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			if script, ok := v["script"].(map[string]any); ok {
				if content, ok := script["content"].(string); ok {
					steps = append(steps, content)
				}
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	// The reader splits on a separator line that carries a comment too, which a
	// pattern for a bare `---` would leave inside one document, and Unmarshal
	// reads only the first document it is given.
	docs := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(src)))
	for {
		doc, err := docs.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("splitting %s into documents: %v", path, err)
		}
		var tree any
		if err := yaml.Unmarshal(doc, &tree); err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		walk(tree)
	}
	return steps
}

// checkSuiteSteps holds each script step that calls the guard to its own read,
// since every step runs in a shell of its own, and returns how many did.
func checkSuiteSteps(t testing.TB, path string, steps []string) int {
	t.Helper()
	callers := 0
	for _, step := range steps {
		consumers := guardConsumers.FindAllSubmatchIndex([]byte(step), -1)
		if len(consumers) == 0 {
			continue
		}
		callers++
		assertConsumedLocalsTakeTheSharedRead(t, path, []byte(step), consumers)
	}
	return callers
}

// errorRecorder collects Errorf calls instead of failing, so a subtest can
// assert that a check reports a fixture it must refuse.
type errorRecorder struct {
	testing.TB
	errs []string
}

func (r *errorRecorder) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

// No shell library but the one that owns the name assigns it. Sourcing the
// owning library resolves what that library ends up with, and cannot see a
// later one overwriting the name: the caller sources the owning library first,
// so an assignment in one sourced after it lands afterwards and wins.
//
// The check is wider than that reasoning. It reads every library beside the
// owning one, including those this caller never sources, because which ones it
// sources is a property of the caller today and not of the name. Widening it
// costs nothing here and stops the check from having to be revisited when a
// caller adds a source line, which is the edit most likely to introduce the
// assignment this forbids.
//
// The set is asserted non-empty before it is used. A glob that stops matching
// turns "no sibling assigns the name" into a statement about nothing, and that
// reads exactly like a pass.
func TestGuardNoSiblingLibraryAssignsTheSharedName(t *testing.T) {
	t.Run("refuses", func(t *testing.T) {
		line := `  HELMRELEASE_HISTORY_JSONPATH='{range .status.history[*]}{.version}{"\n"}{end}'`
		if !nameFollowedByEquals.MatchString(line) {
			t.Errorf("a shadowing assignment went unseen: %s", line)
		}
	})

	t.Run("accepts", func(t *testing.T) {
		line := `  history_statuses=$(kubectl get hr -o"jsonpath=${HELMRELEASE_HISTORY_JSONPATH}")`
		if nameFollowedByEquals.MatchString(line) {
			t.Errorf("a plain read was taken as an assignment: %s", line)
		}
	})

	siblings, err := filepath.Glob(filepath.Join(filepath.Dir(guardLibrary), "*.sh"))
	if err != nil {
		t.Fatalf("globbing the shell libraries: %v", err)
	}
	checked := 0
	for _, path := range siblings {
		if filepath.Clean(path) == filepath.Clean(guardLibrary) {
			continue
		}
		checked++
		src := readGuardFile(t, path)
		if n := len(nameFollowedByEquals.FindAll(src, -1)); n != 0 {
			t.Errorf("%s writes %s followed by = %d times; only %s may assign it. The count is "+
				"of occurrences, so a comment mentioning one counts too; what it looks for is an "+
				"assignment in a library sourced after the one that owns the name, which would "+
				"replace the value this file checked.", path, sharedName, n, guardLibrary)
		}
	}
	if checked == 0 {
		t.Fatalf("no sibling libraries found next to %s, so this test asserted nothing", guardLibrary)
	}
}
