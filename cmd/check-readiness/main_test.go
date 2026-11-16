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
	"io"
	"os"
	"strings"
	"testing"
)

func TestHelpAndVersionSucceedOnWritableStdout(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	stdout := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = stdout })
	read := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		read <- string(b)
	}()

	for _, arg := range []string{"--help", "--version"} {
		if code := run([]string{arg}); code != 0 {
			t.Errorf("run(%s) = %d, want 0", arg, code)
		}
	}
	_ = w.Close()
	if out := <-read; !strings.Contains(out, "Usage:") || !strings.Contains(out, Version) {
		t.Errorf("stdout = %q, want the usage text and the version", out)
	}
}

func TestHelpAndVersionFailOnUnwritableStdout(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	_ = w.Close()
	stdout := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = stdout })

	for _, arg := range []string{"--help", "--version"} {
		if code := run([]string{arg}); code == 0 {
			t.Errorf("run(%s) with an unwritable stdout = 0, want a failure", arg)
		}
	}
}
