// Package checkreadiness_test is a black-box test suite for the check-readiness
// command (cmd/check-readiness).
//
// By default it builds ./cmd/check-readiness and runs that binary against a mock
// kubectl that serves canned fixtures, comparing stdout/stderr/exit against
// golden files. Set SCRIPT_UNDER_TEST to point at any other drop-in executable
// (e.g. an alternative implementation) to validate it against the same suite.
//
// Run:        go test ./test/check-readiness/
// Regenerate: go test ./test/check-readiness/ -update
// Other impl: SCRIPT_UNDER_TEST=/path/to/binary go test ./test/check-readiness/
package checkreadiness_test

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "regenerate golden files")

// defaultBinary is the check-readiness binary built once by TestMain; used as
// the subject under test unless SCRIPT_UNDER_TEST overrides it.
var defaultBinary string

func TestMain(m *testing.M) {
	flag.Parse()
	if os.Getenv("SCRIPT_UNDER_TEST") == "" {
		root, err := repoRootErr()
		if err != nil {
			fmt.Fprintln(os.Stderr, "locate repo root:", err)
			os.Exit(1)
		}
		bin := filepath.Join(os.TempDir(), "check-readiness-test-bin")
		build := exec.Command("go", "build", "-o", bin, "./cmd/check-readiness")
		build.Dir = root
		build.Stderr = os.Stderr
		if err := build.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "build check-readiness:", err)
			os.Exit(1)
		}
		defaultBinary = bin
	}
	os.Exit(m.Run())
}

// elapsedRe normalizes timing-dependent phrases ("Ready after 0s.",
// "Timeout after 1s") so wait-mode goldens are stable.
var elapsedRe = regexp.MustCompile(`(after )\d+(s)`)

func normalize(s string) string {
	return elapsedRe.ReplaceAllString(s, "${1}<N>${2}")
}

// testCase is one black-box invocation.
type testCase struct {
	name     string   // subtest name and golden basename
	args     []string // arguments passed to the script
	fixture  string   // scenario dir under testdata/fixtures (""=base only)
	wantExit int      // expected process exit code
	wantLog  []string // substrings that must appear in the kubectl call log
	dontLog  []string // substrings that must NOT appear in the kubectl call log
}

func cases() []testCase {
	return []testCase{
		// === Argument parsing ===
		{name: "help-short", args: []string{"-h"}, wantExit: 0},
		{name: "help-long", args: []string{"--help"}, wantExit: 0},
		{name: "unknown-arg", args: []string{"--bogus"}, wantExit: 2},
		{name: "timeout-missing-value", args: []string{"--timeout"}, wantExit: 2},
		{name: "timeout-invalid", args: []string{"--timeout", "xyz"}, wantExit: 2},
		{name: "namespace-missing-value", args: []string{"-n"}, wantExit: 2},
		{name: "selector-missing-value", args: []string{"-l"}, wantExit: 2},
		{name: "watch-nonnumeric-interval", args: []string{"-w", "abc"}, wantExit: 2},
		{
			// `-w 5` must consume "5" as the interval; --wait then drives a fast
			// exit. If "5" were not consumed it would be an unknown arg (exit 2).
			name: "watch-numeric-interval-consumed",
			args: []string{"-w", "5", "--wait", "--no-color"}, fixture: "_base", wantExit: 0,
		},

		// === Output behavior ===
		{name: "all-ready", args: []string{"--no-color"}, fixture: "all-ready", wantExit: 0},
		{name: "helmrelease-not-ready", args: []string{"--no-color"}, fixture: "helmrelease-not-ready", wantExit: 1},
		{name: "cluster-scoped-not-ready", args: []string{"--no-color"}, fixture: "node-not-ready", wantExit: 1},
		{name: "suspended-helmrelease", args: []string{"--no-color"}, fixture: "suspended", wantExit: 0},
		{name: "suspended-takes-precedence", args: []string{"--no-color"}, fixture: "suspended-notready", wantExit: 0},
		{name: "pvc-pending", args: []string{"--no-color"}, fixture: "pvc-pending", wantExit: 1},
		{name: "pvc-bound-filtered", args: []string{"--no-color"}, fixture: "pvc-bound", wantExit: 0},
		{name: "condition-unknown-status", args: []string{"--no-color"}, fixture: "condition-unknown", wantExit: 1},
		{name: "condition-missing", args: []string{"--no-color"}, fixture: "condition-missing", wantExit: 1},
		{name: "verbose-reason-message", args: []string{"-v", "--no-color"}, fixture: "verbose", wantExit: 1},
		{name: "verbose-long-message-truncated", args: []string{"-v", "--no-color"}, fixture: "verbose-long", wantExit: 1},
		{name: "crd-not-installed-skipped", args: []string{"--core", "--no-color"}, fixture: "crd-missing", wantExit: 0},
		{name: "mixed-multiple-kinds", args: []string{"--no-color"}, fixture: "mixed", wantExit: 1},

		// === kubectl argument propagation (asserted via call log) ===
		{
			name: "core-skips-extras", args: []string{"--core", "--no-color"}, fixture: "_base", wantExit: 0,
			wantLog: []string{"get packages.cozystack.io", "get kustomizations.kustomize.toolkit.fluxcd.io"},
			dontLog: []string{"get gitrepositories", "get persistentvolumeclaims", "get nodes", "get apiservices"},
		},
		{
			name: "default-uses-all-namespaces", args: []string{"--no-color"}, fixture: "_base", wantExit: 0,
			wantLog: []string{"get helmreleases.helm.toolkit.fluxcd.io -A"},
			dontLog: []string{"get nodes -A"},
		},
		{
			name: "namespace-flag-propagated", args: []string{"-n", "myns", "--no-color"}, fixture: "_base", wantExit: 0,
			wantLog: []string{"get helmreleases.helm.toolkit.fluxcd.io -n myns"},
			dontLog: []string{"get nodes -n myns", "get helmreleases.helm.toolkit.fluxcd.io -A"},
		},
		{
			name: "selector-flag-propagated", args: []string{"-l", "app=demo", "--no-color"}, fixture: "_base", wantExit: 0,
			wantLog: []string{"get helmreleases.helm.toolkit.fluxcd.io -A -l app=demo", "get nodes -l app=demo"},
		},

		// === Wait / timeout ===
		{name: "wait-success", args: []string{"--wait", "--no-color"}, fixture: "_base", wantExit: 0},
		{
			name: "wait-timeout", args: []string{"-w", "1", "--wait", "--timeout", "1s", "--no-color"},
			fixture: "helmrelease-not-ready", wantExit: 1,
		},

		// === Edge cases ===
		{name: "empty-cluster", args: []string{"--no-color"}, fixture: "_base", wantExit: 0},
		{
			name: "api-resources-unavailable", args: []string{"--no-color"}, fixture: "api-resources-empty", wantExit: 0,
			wantLog: []string{"api-resources", "get packages.cozystack.io"},
		},
	}
}

func TestCheckReadiness(t *testing.T) {
	repoRoot := repoRoot(t)
	fakeBin := filepath.Join(repoRoot, "test", "check-readiness", "fakekubectl")
	fixturesRoot := filepath.Join(repoRoot, "test", "check-readiness", "testdata", "fixtures")
	goldenRoot := filepath.Join(repoRoot, "test", "check-readiness", "testdata", "golden")

	// Ensure the mock is executable (git preserves the bit, but be defensive).
	if err := os.Chmod(filepath.Join(fakeBin, "kubectl"), 0o755); err != nil {
		t.Fatalf("chmod mock kubectl: %v", err)
	}

	for _, tc := range cases() {
		t.Run(tc.name, func(t *testing.T) {
			logFile := filepath.Join(t.TempDir(), "kubectl.log")

			fixtureDir := fixturesRoot
			if tc.fixture != "" {
				fixtureDir = filepath.Join(fixturesRoot, tc.fixture)
			}

			cmd := scriptCommand(tc.args)
			cmd.Env = testEnv(fakeBin, fixtureDir, filepath.Join(fixturesRoot, "_base"), logFile)

			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			runErr := cmd.Run()

			gotExit := exitCode(t, runErr)
			if gotExit != tc.wantExit {
				t.Errorf("exit code = %d, want %d\nstderr:\n%s", gotExit, tc.wantExit, stderr.String())
			}

			checkGolden(t, goldenRoot, tc.name+".stdout", normalize(stdout.String()))
			checkGolden(t, goldenRoot, tc.name+".stderr", normalize(stderr.String()))

			assertLog(t, logFile, tc.wantLog, tc.dontLog)
		})
	}
}

// subjectArgv is the full argv to invoke the subject under test: the
// SCRIPT_UNDER_TEST override if set, otherwise the binary built by TestMain.
func subjectArgv(args []string) []string {
	subject := defaultBinary
	if sut := os.Getenv("SCRIPT_UNDER_TEST"); sut != "" {
		subject = sut
	}
	return append([]string{subject}, args...)
}

// scriptCommand builds the command to run the subject under test.
func scriptCommand(args []string) *exec.Cmd {
	argv := subjectArgv(args)
	return exec.Command(argv[0], argv[1:]...)
}

// testEnv returns a clean environment: fake kubectl first on PATH, fixture
// pointers set, KUBECONFIG stripped so the script uses a bare `kubectl`.
func testEnv(fakeBin, fixtureDir, fixtureBase, logFile string) []string {
	var env []string
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "PATH="):
			env = append(env, "PATH="+fakeBin+string(os.PathListSeparator)+kv[len("PATH="):])
		case strings.HasPrefix(kv, "KUBECONFIG="),
			strings.HasPrefix(kv, "FIXTURE_DIR="),
			strings.HasPrefix(kv, "FIXTURE_BASE="),
			strings.HasPrefix(kv, "KUBECTL_LOG="):
			// drop — we set these ourselves
		default:
			env = append(env, kv)
		}
	}
	return append(env,
		"FIXTURE_DIR="+fixtureDir,
		"FIXTURE_BASE="+fixtureBase,
		"KUBECTL_LOG="+logFile,
	)
}

func exitCode(t *testing.T, err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	t.Fatalf("running script: %v", err)
	return -1
}

func checkGolden(t *testing.T, dir, name, got string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if *update {
		if got == "" {
			// Don't litter the tree with empty goldens; remove any stale one.
			_ = os.Remove(path)
			return
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", name, err)
		}
		return
	}

	want := ""
	if b, err := os.ReadFile(path); err == nil {
		want = string(b)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read golden %s: %v", name, err)
	}
	if got != want {
		t.Errorf("%s mismatch:\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}

func assertLog(t *testing.T, logFile string, want, dont []string) {
	t.Helper()
	if len(want) == 0 && len(dont) == 0 {
		return
	}
	b, err := os.ReadFile(logFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read kubectl log: %v", err)
	}
	log := string(b)
	for _, sub := range want {
		if !strings.Contains(log, sub) {
			t.Errorf("kubectl log missing %q\nlog:\n%s", sub, log)
		}
	}
	for _, sub := range dont {
		if strings.Contains(log, sub) {
			t.Errorf("kubectl log unexpectedly contains %q\nlog:\n%s", sub, log)
		}
	}
}

// TestColorTTY verifies the script emits ANSI color when stdout is a real TTY
// and suppresses it under --no-color. The byte-level golden cases above all run
// through a pipe (non-TTY), where color auto-disables, so this is the only place
// the color path is exercised. Linux-only; requires util-linux `script`.
func TestColorTTY(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("PTY color check is Linux-only")
	}
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("`script` not available; skipping PTY color check")
	}

	root := repoRoot(t)
	fakeBin := filepath.Join(root, "test", "check-readiness", "fakekubectl")
	fixturesRoot := filepath.Join(root, "test", "check-readiness", "testdata", "fixtures")
	fixtureDir := filepath.Join(fixturesRoot, "helmrelease-not-ready")
	base := filepath.Join(fixturesRoot, "_base")

	runUnderPTY := func(args []string) string {
		cmdline := strings.Join(subjectArgv(args), " ")
		// script -q (quiet) -e (return child's exit) -c CMD <typescript-file>
		cmd := exec.Command("script", "-qec", cmdline, "/dev/null")
		cmd.Env = testEnv(fakeBin, fixtureDir, base, filepath.Join(t.TempDir(), "kubectl.log"))
		out, _ := cmd.CombinedOutput() // non-zero exit expected (not-ready -> 1)
		return string(out)
	}

	const esc = "\x1b["
	if got := runUnderPTY(nil); !strings.Contains(got, esc) {
		t.Errorf("expected ANSI escapes on a TTY, got none:\n%q", got)
	}
	if got := runUnderPTY([]string{"--no-color"}); strings.Contains(got, esc) {
		t.Errorf("expected no ANSI escapes with --no-color, found some:\n%q", got)
	}
}

func repoRootErr() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := repoRootErr()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	return root
}
