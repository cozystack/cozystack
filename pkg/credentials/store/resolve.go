// SPDX-License-Identifier: Apache-2.0

package store

import (
	"encoding/json"
	"fmt"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	"github.com/cozystack/cozystack/pkg/credentials/account"
	"github.com/cozystack/cozystack/pkg/specschema"
)

// ResolveAccounts resolves the tenant accounts an application declares from
// its values with the kind's schema defaults applied: stored values carry no
// defaults, and a Static account switched on by a defaulted flag would
// otherwise go unseeded.
func ResolveAccounts(block *cozyv1alpha1.ApplicationDefinitionCredentials, schema *structuralschema.Structural, values *apiextv1.JSON) ([]account.Account, error) {
	if block == nil {
		return nil, nil
	}
	m := map[string]any{}
	if values != nil && len(values.Raw) > 0 {
		if err := json.Unmarshal(values.Raw, &m); err != nil {
			return nil, fmt.Errorf("decode values: %w", err)
		}
	}
	if schema != nil {
		if err := specschema.ApplyDefaults(&m, schema); err != nil {
			return nil, fmt.Errorf("default values: %w", err)
		}
	}
	return account.Resolve(block, m)
}
