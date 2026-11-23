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

import (
	"fmt"

	"github.com/cozystack/cozystack/pkg/apis/apps/defaulting"
)

// CompileDefinition compiles the consumption declaration of the kind named
// kind, whose OpenAPI schema is openAPISchema, as an ApplicationDefinition
// carries both. The error names the kind, so that it can be reported as is.
func CompileDefinition(kind string, decl *Declaration, openAPISchema string) (*Evaluator, error) {
	schema, err := defaulting.BuildSchema(openAPISchema)
	if err != nil {
		return nil, fmt.Errorf("the ApplicationDefinition of kind %s: schema: %w", kind, err)
	}
	ev, err := Compile(decl, schema)
	if err != nil {
		return nil, fmt.Errorf("the ApplicationDefinition of kind %s: consumption: %w", kind, err)
	}
	return ev, nil
}
