package kubernetesnodes

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

// A client built on these types writes every field it does not omit. The
// paths it writes as null are listed in testdata/null-paths.json, which the
// pool admission tests read as the inputs that must mean "not set";
// a new null here fails until they cover it too.
func TestTypedClientWritesOnlyTheListedNulls(t *testing.T) {
	raw, err := os.ReadFile("testdata/null-paths.json")
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	if err = json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	for name, spec := range map[string]ConfigSpec{
		"zero":            {},
		"required fields": {Cluster: "k", Version: "v1.35"},
	} {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if err = json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			got := nullPaths("", decoded)
			sort.Strings(got)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("null paths %v, want %v", got, want)
			}
		})
	}
}

func nullPaths(prefix string, value any) []string {
	var out []string
	switch v := value.(type) {
	case nil:
		return []string{prefix}
	case map[string]any:
		for key, member := range v {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			out = append(out, nullPaths(path, member)...)
		}
	case []any:
		for _, member := range v {
			out = append(out, nullPaths(prefix+"[]", member)...)
		}
	}
	return out
}
