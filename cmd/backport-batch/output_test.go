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
	"errors"
	"strings"
	"testing"
	"time"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// Output that never reached its reader fails the command, and stderr says why.
func TestUnwritableOutputFailsTheCommand(t *testing.T) {
	var stderr strings.Builder
	a := &app{gh: &fakeGH{}, stdout: failingWriter{}, stderr: &stderr}
	if code := a.main([]string{"help"}); code != 2 {
		t.Fatalf("help with an unwritable stdout = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "broken pipe") {
		t.Errorf("stderr does not name the write failure:\n%s", stderr.String())
	}
}

// A command that already failed keeps its own exit code when its output is
// lost as well: check still says something is missing.
func TestUnwritableOutputKeepsTheCommandsFailure(t *testing.T) {
	f := newFixture(t)
	f.lines()
	f.pr(6, "fix(app): six", "", map[string]string{"app/six.txt": "6\n"})
	var stderr strings.Builder
	a := &app{gh: &fakeGH{remoteHead: f.remoteHead}, stdout: failingWriter{}, stderr: &stderr, now: func() time.Time { return f.now }, tool: f.tool}
	if code := a.main([]string{"check", "--line", "release-1.1"}); code != 1 {
		t.Fatalf("check with #6 missing and an unwritable stdout = %d, want 1\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "broken pipe") {
		t.Errorf("stderr does not name the write failure:\n%s", stderr.String())
	}
}
