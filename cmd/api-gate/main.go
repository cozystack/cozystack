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

// Command api-gate compares the Cozystack API surface between two checkouts of
// the repository and reports whether the change is "sizeable" — a new API
// group, a new resource, or a breaking change to an existing resource. CI uses
// a sizeable verdict to require a review from a designated API owner.
//
// Usage:
//
//	api-gate --base <dir> --head <dir>
//
// Both flags point at a full repository checkout (the merge base and the PR
// head). Exit code 0 means "not sizeable"; exit code 2 means "sizeable"
// (findings are printed to stdout as a Markdown report); any other non-zero
// exit is an operational error.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/cozystack/cozystack/internal/apigate"
)

// exitSizeable is a distinct, non-error exit code so the calling workflow can
// tell "sizeable change, needs owner review" apart from a tool malfunction.
const exitSizeable = 2

func main() {
	baseDir := flag.String("base", "", "path to the base (merge-base) repository checkout")
	headDir := flag.String("head", "", "path to the head (PR) repository checkout")
	format := flag.String("format", "markdown", "report format: markdown or text")
	flag.Parse()

	if *baseDir == "" || *headDir == "" {
		fmt.Fprintln(os.Stderr, "api-gate: both --base and --head are required")
		flag.Usage()
		os.Exit(1)
	}

	base, err := apigate.LoadSnapshot(*baseDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "api-gate: loading base snapshot: %v\n", err)
		os.Exit(1)
	}
	head, err := apigate.LoadSnapshot(*headDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "api-gate: loading head snapshot: %v\n", err)
		os.Exit(1)
	}

	findings := apigate.Classify(base, head)
	if len(findings) == 0 {
		fmt.Println("No sizeable API changes detected.")
		return
	}

	fmt.Print(apigate.Report(findings, *format))
	os.Exit(exitSizeable)
}
