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

// Command check-readiness checks readiness of cozystack / Flux / Kubernetes
// resources. It shells out to kubectl (resolved from PATH) and reports only
// non-ready resources, parsed from status conditions rather than column
// heuristics.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Version is set at build time via -ldflags "-X main.Version=...".
var Version = "dev"

// usageText is shown by -h/--help. Kept byte-for-byte identical to the golden
// files in test/check-readiness/testdata/golden.
const usageText = `Check readiness of cozystack / Flux / Kubernetes resources.
Shows only non-ready resources, parsed from status conditions (not column heuristics).

Fetches sequentially by default — intended to run during cozystack upgrades where
the apiserver is already under heavy reconciler load, so we avoid bursts of N
concurrent LIST requests. Use --parallel for one-shot interactive runs against a
healthy cluster (3-4x faster wall-clock).

Usage:
  check-readiness [OPTIONS]

Options:
  -w, --watch [INTERVAL]    Watch mode; refresh every INTERVAL seconds (default: 10)
      --wait                Block until everything is ready (exit 0) or timeout (exit 1)
      --timeout DURATION    Timeout for --wait, e.g. 30m, 1h, 600s (default: 30m)
      --parallel            Fire all fetches concurrently (faster, more apiserver load)
      --core                Check only essential cozystack/Flux kinds (5 instead of 14)
  -v, --verbose             Show condition reason/message for not-ready rows
  -n, --namespace NS        Scope to a single namespace (cluster-scoped kinds ignore this)
  -l, --selector SELECTOR   kubectl label selector to apply to every query
      --no-color            Disable color output (auto-disabled on non-TTY)
  -h, --help                Show this help

`

// entry is a resource catalog item.
type entry struct {
	kind            string
	scope           string // "namespaced" | "cluster"
	condType        string // condition .type, or "_phase" for PVCs
	supportsSuspend bool
}

var coreKinds = []entry{
	{"packages.cozystack.io", "cluster", "Ready", false},
	{"artifactgenerators.source.extensions.fluxcd.io", "namespaced", "Ready", true},
	{"externalartifacts.source.toolkit.fluxcd.io", "namespaced", "Ready", false},
	{"helmreleases.helm.toolkit.fluxcd.io", "namespaced", "Ready", true},
	{"kustomizations.kustomize.toolkit.fluxcd.io", "namespaced", "Ready", true},
}

var fluxExtraKinds = []entry{
	{"gitrepositories.source.toolkit.fluxcd.io", "namespaced", "Ready", true},
	{"ocirepositories.source.toolkit.fluxcd.io", "namespaced", "Ready", true},
	{"helmrepositories.source.toolkit.fluxcd.io", "namespaced", "Ready", true},
	{"helmcharts.source.toolkit.fluxcd.io", "namespaced", "Ready", true},
	{"buckets.source.toolkit.fluxcd.io", "namespaced", "Ready", true},
}

var clusterKinds = []entry{
	{"nodes", "cluster", "Ready", false},
	{"apiservices.apiregistration.k8s.io", "cluster", "Available", false},
	{"customresourcedefinitions.apiextensions.k8s.io", "cluster", "Established", false},
}

var pvcKind = entry{"persistentvolumeclaims", "namespaced", "_phase", false}

type config struct {
	kubectl []string

	watch     bool
	interval  int
	waitMode  bool
	timeoutS  int
	verbose   bool
	namespace string
	selector  string
	useColor  bool
	parallel  bool
	coreOnly  bool

	// colors
	red, green, yellow, cyan, dim, bold, reset string

	// cached api-resources
	apiCache  string
	apiCached bool

	foundIssues bool
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cfg := &config{
		kubectl:  []string{"kubectl"},
		interval: 10,
		timeoutS: 0,
		useColor: true,
	}
	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		cfg.kubectl = append(cfg.kubectl, "--kubeconfig="+kc)
	}

	timeoutRaw := "30m"

	i := 0
	for i < len(args) {
		a := args[i]
		switch a {
		case "-w", "--watch":
			cfg.watch = true
			if i+1 < len(args) && isAllDigits(args[i+1]) {
				cfg.interval, _ = strconv.Atoi(args[i+1])
				i++
			}
			i++
		case "--wait":
			cfg.waitMode = true
			i++
		case "--timeout":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "--timeout requires a value")
				return 2
			}
			timeoutRaw = args[i+1]
			i += 2
		case "-v", "--verbose":
			cfg.verbose = true
			i++
		case "-n", "--namespace":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "--namespace requires a value")
				return 2
			}
			cfg.namespace = args[i+1]
			i += 2
		case "-l", "--selector":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "--selector requires a value")
				return 2
			}
			cfg.selector = args[i+1]
			i += 2
		case "--no-color":
			cfg.useColor = false
			i++
		case "--parallel":
			cfg.parallel = true
			i++
		case "--core":
			cfg.coreOnly = true
			i++
		case "-h", "--help":
			fmt.Fprint(os.Stdout, usageText)
			return 0
		case "--version":
			fmt.Fprintln(os.Stdout, Version)
			return 0
		default:
			fmt.Fprintf(os.Stderr, "Unknown argument: %s\n", a)
			fmt.Fprint(os.Stderr, usageText)
			return 2
		}
	}

	// Auto-disable color on non-TTY.
	if fi, err := os.Stdout.Stat(); err == nil {
		if fi.Mode()&os.ModeCharDevice == 0 {
			cfg.useColor = false
		}
	} else {
		cfg.useColor = false
	}

	cfg.setColors()

	// parse_timeout runs unconditionally on every invocation.
	sec, ok := parseTimeout(timeoutRaw)
	if !ok {
		fmt.Fprintf(os.Stderr, "Invalid --timeout value: %s (expected e.g. 30m, 1h, 600s)\n", timeoutRaw)
		return 2
	}
	cfg.timeoutS = sec

	if cfg.waitMode {
		return cfg.runWait()
	}
	if cfg.watch {
		cfg.runWatch()
		return 0
	}
	out := cfg.runOnce()
	fmt.Print(out)
	if cfg.foundIssues {
		return 1
	}
	return 0
}

func (cfg *config) setColors() {
	if cfg.useColor {
		cfg.red = "\033[0;31m"
		cfg.green = "\033[0;32m"
		cfg.yellow = "\033[1;33m"
		cfg.cyan = "\033[0;36m"
		cfg.dim = "\033[2m"
		cfg.bold = "\033[1m"
		cfg.reset = "\033[0m"
	}
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseTimeout accepts ^[0-9]+[smh]?$ (bare number = seconds).
func parseTimeout(raw string) (int, bool) {
	if raw == "" {
		return 0, false
	}
	unit := byte('s')
	digits := raw
	last := raw[len(raw)-1]
	if last == 's' || last == 'm' || last == 'h' {
		unit = last
		digits = raw[:len(raw)-1]
	}
	if !isAllDigits(digits) {
		return 0, false
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return 0, false
	}
	switch unit {
	case 's':
		return n, true
	case 'm':
		return n * 60, true
	case 'h':
		return n * 3600, true
	}
	return 0, false
}

func (cfg *config) runWait() int {
	start := time.Now()
	for {
		output := cfg.runOnce()
		elapsed := int(time.Since(start).Seconds())
		if strings.Contains(output, "All resources are ready.") {
			fmt.Print(output)
			fmt.Printf("%sReady after %ds.%s\n", cfg.green, elapsed, cfg.reset)
			return 0
		}
		if elapsed >= cfg.timeoutS {
			fmt.Print(output)
			fmt.Fprintf(os.Stderr, "%s%sTimeout after %ds — resources still not ready.%s\n", cfg.red, cfg.bold, cfg.timeoutS, cfg.reset)
			return 1
		}
		time.Sleep(time.Duration(cfg.interval) * time.Second)
	}
}

func (cfg *config) runWatch() {
	for {
		output := cfg.runOnce()
		// Smooth clear is cosmetic and untested; just reprint.
		fmt.Printf("%sLast updated: %s  (refreshing every %ds, Ctrl+C to stop)%s\n", cfg.bold, time.Now().Format(time.UnixDate), cfg.interval, cfg.reset)
		fmt.Println()
		fmt.Print(output)
		time.Sleep(time.Duration(cfg.interval) * time.Second)
	}
}

// buildScopeArgs mirrors build_kubectl_scope_args.
func (cfg *config) buildScopeArgs(scope string) []string {
	var out []string
	if scope == "namespaced" {
		if cfg.namespace != "" {
			out = append(out, "-n", cfg.namespace)
		} else {
			out = append(out, "-A")
		}
	}
	if cfg.selector != "" {
		out = append(out, "-l", cfg.selector)
	}
	return out
}

func (cfg *config) ensureAPIResources() {
	if cfg.apiCached {
		return
	}
	args := append(append([]string{}, cfg.kubectl[1:]...), "api-resources", "--no-headers")
	cmd := exec.Command(cfg.kubectl[0], args...)
	out, err := cmd.Output()
	if err != nil {
		cfg.apiCache = ""
	} else {
		cfg.apiCache = string(out)
	}
	cfg.apiCached = true
}

// kindExists checks the cached api-resources output.
func (cfg *config) kindExists(kind string) bool {
	cfg.ensureAPIResources()
	if cfg.apiCache == "" {
		return true
	}
	name := kind
	group := ""
	if idx := strings.IndexByte(kind, '.'); idx >= 0 {
		name = kind[:idx]
		group = kind[idx+1:]
	}
	for _, line := range strings.Split(cfg.apiCache, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if fields[0] != name {
			continue
		}
		apiv := fields[len(fields)-3]
		grp := ""
		if parts := strings.SplitN(apiv, "/", 2); len(parts) == 2 {
			grp = parts[0]
		}
		if group == "" || group == grp {
			return true
		}
	}
	return false
}

// fetchRows runs kubectl get and returns raw jsonpath output.
func (cfg *config) fetchRows(e entry) string {
	scopeArgs := cfg.buildScopeArgs(e.scope)

	var jsonpath string
	if e.condType == "_phase" {
		jsonpath = `{range .items[*]}{.metadata.namespace}{"\x1f"}{.metadata.name}{"\x1f"}{.status.phase}{"\x1f"}{""}{"\x1f"}{""}{"\x1f"}{"false"}{"\n"}{end}`
	} else {
		suspendField := `{"false"}`
		if e.supportsSuspend {
			suspendField = `{.spec.suspend}`
		}
		var b strings.Builder
		b.WriteString(`{range .items[*]}`)
		b.WriteString(`{.metadata.namespace}{"\x1f"}`)
		b.WriteString(`{.metadata.name}{"\x1f"}`)
		b.WriteString(`{.status.conditions[?(@.type=="` + e.condType + `")].status}{"\x1f"}`)
		b.WriteString(`{.status.conditions[?(@.type=="` + e.condType + `")].reason}{"\x1f"}`)
		b.WriteString(`{.status.conditions[?(@.type=="` + e.condType + `")].message}{"\x1f"}`)
		b.WriteString(suspendField + `{"\n"}`)
		b.WriteString(`{end}`)
		jsonpath = b.String()
	}

	args := append([]string{}, cfg.kubectl[1:]...)
	args = append(args, "get", e.kind)
	args = append(args, scopeArgs...)
	args = append(args, "-o", "jsonpath="+jsonpath)

	cmd := exec.Command(cfg.kubectl[0], args...)
	out, _ := cmd.Output() // ignore errors, like `|| true`
	return string(out)
}

// formatExtra mirrors format_extra.
func formatExtra(reason, message string) string {
	extra := ""
	if reason != "" && reason != "<none>" {
		extra = reason
	}
	if message != "" && message != "<none>" {
		trimmed := message
		if idx := strings.IndexByte(trimmed, '\n'); idx >= 0 {
			trimmed = trimmed[:idx]
		}
		trimmed = cutChars(trimmed, 120)
		if extra != "" {
			extra = extra + ": " + trimmed
		} else {
			extra = trimmed
		}
	}
	return extra
}

// cutChars mimics `cut -c1-N` (byte-based for single-byte; cut counts bytes in
// the C locale). Use runes to be safe with the test data which is ASCII.
func cutChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// processRows mirrors process_rows; appends rendered output to b.
func (cfg *config) processRows(e entry, rows string, b *strings.Builder) {
	if rows == "" {
		return
	}
	headerPrinted := false
	notReady := 0
	total := 0

	for _, line := range strings.Split(rows, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\x1f")
		// ns name status reason message suspended
		var ns, name, status, reason, message, suspended string
		if len(fields) > 0 {
			ns = fields[0]
		}
		if len(fields) > 1 {
			name = fields[1]
		}
		if len(fields) > 2 {
			status = fields[2]
		}
		if len(fields) > 3 {
			reason = fields[3]
		}
		if len(fields) > 4 {
			message = fields[4]
		}
		if len(fields) > 5 {
			suspended = fields[5]
		}

		if name == "" {
			continue
		}
		total++

		label := ""
		color := ""
		if suspended == "true" {
			label = "SUSPENDED"
			color = cfg.cyan
		} else if e.condType == "_phase" {
			if status != "Bound" {
				label = status
				color = cfg.red
			}
		} else {
			if status != "True" {
				if status == "" {
					label = "Unknown"
				} else {
					label = status
				}
				color = cfg.red
			}
		}

		if label == "" {
			continue
		}

		if !headerPrinted {
			fmt.Fprintf(b, "%s%s=== %s (not ready) ===%s\n", cfg.bold, cfg.yellow, e.kind, cfg.reset)
			headerPrinted = true
		}
		notReady++

		var prefix string
		if ns != "" {
			prefix = fmt.Sprintf("%-30s %-50s %s", ns, name, label)
		} else {
			prefix = fmt.Sprintf("%-50s %s", name, label)
		}
		fmt.Fprintf(b, "%s%s%s\n", color, prefix, cfg.reset)

		if cfg.verbose {
			extra := formatExtra(reason, message)
			if extra != "" {
				fmt.Fprintf(b, "  %s%s%s\n", cfg.dim, extra, cfg.reset)
			}
		}

		if label != "SUSPENDED" {
			cfg.foundIssues = true
		}
	}

	if headerPrinted && cfg.verbose {
		fmt.Fprintf(b, "%s  -> %d/%d not ready%s\n", cfg.dim, notReady, total, cfg.reset)
	}
}

func (cfg *config) runOnce() string {
	cfg.foundIssues = false
	cfg.ensureAPIResources()

	entries := append([]entry{}, coreKinds...)
	if !cfg.coreOnly {
		entries = append(entries, fluxExtraKinds...)
		entries = append(entries, clusterKinds...)
		entries = append(entries, pvcKind)
	}

	rowsByIdx := make([]string, len(entries))

	// Phase 1: fetch.
	if cfg.parallel {
		var wg sync.WaitGroup
		for idx := range entries {
			e := entries[idx]
			if !cfg.kindExists(e.kind) {
				continue
			}
			wg.Add(1)
			go func(idx int, e entry) {
				defer wg.Done()
				rowsByIdx[idx] = cfg.fetchRows(e)
			}(idx, e)
		}
		wg.Wait()
	} else {
		for idx := range entries {
			e := entries[idx]
			if !cfg.kindExists(e.kind) {
				continue
			}
			rowsByIdx[idx] = cfg.fetchRows(e)
		}
	}

	var b strings.Builder

	// Phase 2: process in catalog order.
	for idx := range entries {
		e := entries[idx]
		if !cfg.kindExists(e.kind) {
			fmt.Fprintf(&b, "%s--- %s: CRD not installed, skipping%s\n", cfg.dim, e.kind, cfg.reset)
			continue
		}
		cfg.processRows(e, rowsByIdx[idx], &b)
	}

	if !cfg.foundIssues {
		fmt.Fprintf(&b, "%s%sAll resources are ready.%s\n", cfg.green, cfg.bold, cfg.reset)
	}

	return b.String()
}
