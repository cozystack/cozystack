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

package reservation

import "fmt"

// Application is one stored application as the aggregator sees it: the
// labelled HelmRelease the aggregated apiserver persisted, reduced to what
// identifies its definition and its undefaulted values.
type Application struct {
	Namespace string
	Name      string
	Kind      string
	Group     string
	Values    []byte
}

// EvaluatorLookup returns the evaluator of a kind, found by its kind and
// group so that an out-of-tree kind resolves through the same path as an
// in-tree one.
type EvaluatorLookup func(kind, group string) (*Evaluator, error)

// Failure is an application whose reservation could not be computed.
type Failure struct {
	Application Application
	Err         error
}

func (f Failure) Error() string {
	return fmt.Sprintf("%s %s/%s: %v", f.Application.Kind, f.Application.Namespace, f.Application.Name, f.Err)
}

// Aggregate sums the reservations of apps per namespace.
//
// An application that cannot be evaluated is returned as a Failure and is
// left out of the sums. The sums are therefore a lower bound whenever a
// failure is returned, and the caller must not treat them as complete: a
// stored application that stops evaluating has to keep its last known
// reservation rather than hand its share of the pool to the next writer.
func Aggregate(apps []Application, lookup EvaluatorLookup, env Environment) (map[string]ResourceList, []Failure) {
	perNamespace := map[string]ResourceList{}
	var failures []Failure
	for _, app := range apps {
		ev, err := lookup(app.Kind, app.Group)
		if err != nil {
			failures = append(failures, Failure{Application: app, Err: err})
			continue
		}
		rl, err := ev.EvaluateJSON(app.Values, env)
		if err != nil {
			failures = append(failures, Failure{Application: app, Err: err})
			continue
		}
		sum, ok := perNamespace[app.Namespace]
		if !ok {
			sum = ResourceList{}
			perNamespace[app.Namespace] = sum
		}
		sum.Add(rl)
	}
	return perNamespace, failures
}
