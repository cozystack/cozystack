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

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestPrintReportReturnsWriteError(t *testing.T) {
	r := &Report{Findings: []Finding{{Severity: SeverityError, Code: "X", Message: "m"}}}
	if err := printReport(r, failingWriter{}); err == nil {
		t.Fatal("printReport to an unwritable writer returned nil")
	}
}

// The table is buffered until the tabwriter flushes, so a failing reader shows
// up only as a Flush error, which list has to return.
func TestListReturnsWriteError(t *testing.T) {
	s := runtime.NewScheme()
	utilruntime.Must(cozyv1alpha1.AddToScheme(s))
	ps := &cozyv1alpha1.PackageSource{ObjectMeta: metav1.ObjectMeta{Name: "acme.app"}}
	pkg := &cozyv1alpha1.Package{ObjectMeta: metav1.ObjectMeta{Name: "acme.app"}}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(ps, pkg).Build()

	for name, list := range map[string]func(io.Writer) error{
		"sources":  func(w io.Writer) error { return listPackageSources(context.Background(), cl, w, true) },
		"packages": func(w io.Writer) error { return listPackages(context.Background(), cl, w, true) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := list(failingWriter{}); err == nil {
				t.Fatal("list to an unwritable writer returned nil")
			}
			var out strings.Builder
			if err := list(&out); err != nil || !strings.Contains(out.String(), "acme.app") {
				t.Fatalf("list = %v, output %q; want the row", err, out.String())
			}
		})
	}
}

// A command that cannot print its validation report still fails, and a
// refusal keeps validation as its reason with the write error joined to it.
func TestValidationCommandsReportUnwritableReport(t *testing.T) {
	fakeFlux := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeFlux, "flux"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeFlux+string(os.PathListSeparator)+os.Getenv("PATH"))

	emptyRepo := func(t *testing.T) string {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "packages"), 0o755); err != nil {
			t.Fatal(err)
		}
		return root
	}
	run := func(c *cobra.Command, args ...string) error {
		c.SetOut(failingWriter{})
		t.Cleanup(func() { c.SetOut(nil) })
		return c.RunE(c, args)
	}
	push := func(root string) error {
		pushCmdFlags.path = root
		t.Cleanup(func() { pushCmdFlags.path = "." })
		return run(pushCmd, "oci://registry.example/acme/apps:v1")
	}

	for _, tc := range []struct {
		name    string
		run     func(t *testing.T) error
		refusal string
	}{
		{name: "validate, valid", run: func(t *testing.T) error { return run(validateCmd, validRepo(t)) }},
		{name: "validate, invalid", run: func(t *testing.T) error { return run(validateCmd, emptyRepo(t)) }, refusal: "validation failed"},
		{name: "push, valid", run: func(t *testing.T) error { return push(validRepo(t)) }, refusal: "nothing was pushed"},
		{name: "push, invalid", run: func(t *testing.T) error { return push(emptyRepo(t)) }, refusal: "refusing to push"},
		{name: "tap, invalid", run: func(t *testing.T) error { return run(tapCmd, "oci://registry.example/acme/apps:v1") }, refusal: "refusing to tap"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(t)
			if err == nil || !strings.Contains(err.Error(), "broken pipe") {
				t.Fatalf("err = %v, want the write error", err)
			}
			if tc.refusal != "" && !strings.Contains(err.Error(), tc.refusal) {
				t.Errorf("err = %v, want the refusal %q kept", err, tc.refusal)
			}
		})
	}
}

// A tap that applied is reported as applied even when its summary is lost,
// and the lost summary still fails the command.
func TestTapSummaryReportsUnwritableOutput(t *testing.T) {
	err := writeTapSummary(failingWriter{}, "oci://registry.example/acme/apps:v1", "tap-acme", []string{"acme.app"})
	if err == nil || !strings.Contains(err.Error(), "tap applied") || !strings.Contains(err.Error(), "broken pipe") {
		t.Fatalf("err = %v, want the write error with the tap reported as applied", err)
	}
	var out strings.Builder
	if err := writeTapSummary(&out, "oci://registry.example/acme/apps:v1", "tap-acme", []string{"acme.app"}); err != nil || !strings.Contains(out.String(), "cozypkg add acme.app") {
		t.Fatalf("writeTapSummary = %v, output %q", err, out.String())
	}
}

// A clean report has no finding lines, so its summary is its only write and
// alone decides the result.
func TestPrintReportReturnsSummaryWriteError(t *testing.T) {
	if err := printReport(&Report{}, failingWriter{}); err == nil {
		t.Fatal("printReport of a clean report to an unwritable writer returned nil")
	}
}

// push runs flux only after its status line was written, with validation
// skipped and after a report that was written.
func TestPushStopsBeforeFluxWhenItsStatusIsLost(t *testing.T) {
	fakeFlux := t.TempDir()
	ran := filepath.Join(t.TempDir(), "flux-ran")
	if err := os.WriteFile(filepath.Join(fakeFlux, "flux"), []byte("#!/bin/sh\ntouch "+ran+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeFlux+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, skip := range []bool{true, false} {
		t.Run(fmt.Sprintf("skip-validate=%v", skip), func(t *testing.T) {
			pushCmdFlags.path, pushCmdFlags.skipValidate = validRepo(t), skip
			pushCmdFlags.source, pushCmdFlags.revision = "https://example.com/acme.git", "v1"
			t.Cleanup(func() {
				pushCmdFlags.path, pushCmdFlags.skipValidate = ".", false
				pushCmdFlags.source, pushCmdFlags.revision = "", ""
			})
			pushCmd.SetOut(&failOn{want: "Pushing"})
			t.Cleanup(func() { pushCmd.SetOut(nil) })
			err := pushCmd.RunE(pushCmd, []string{"oci://registry.example/acme/apps:v1"})
			if err == nil || !strings.Contains(err.Error(), "nothing was pushed") {
				t.Fatalf("push losing its status = %v, want nothing pushed", err)
			}
			if _, statErr := os.Stat(ran); statErr == nil {
				t.Fatal("flux ran after the status line was lost")
			}
		})
	}
}

// failOn fails every write that contains want, and takes every other one.
type failOn struct {
	want string
	buf  strings.Builder
}

func (w *failOn) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.want) {
		return 0, errors.New("broken pipe")
	}
	return w.buf.Write(p)
}
