package admission

import (
	"context"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	av1 "k8s.io/api/admission/v1"
)

// The chart refuses an adoption that renames the pool's bootstrap config or
// machine template. A pool field the templates read directly, rather than
// through Resolve, is invisible to the equivalence check, so the webhook must
// hold the same set: an adoption it admits and the chart then refuses leaves
// the class stored on a pool that cannot render.
func TestAdoptionHoldsEveryPoolFieldTheTemplatesRead(t *testing.T) {
	chart := "../../../packages/apps/kubernetes-nodes/templates/"
	read := func(file string) string {
		data, err := os.ReadFile(chart + file)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	body := func(text, define string) string {
		start := strings.Index(text, `{{- define "`+define+`" -}}`)
		if start < 0 {
			t.Fatalf("define %q not found", define)
		}
		end := strings.Index(text[start:], "\n{{- end }}\n")
		if end < 0 {
			t.Fatalf("end of define %q not found", define)
		}
		return text[start : start+end]
	}
	fields := map[string]bool{}
	collect := func(pattern, text string) {
		for _, m := range regexp.MustCompile(pattern).FindAllStringSubmatch(text, -1) {
			fields[m[1]] = true
		}
	}
	// Every define in the chart's templates, so a helper the scanned
	// templates include is scanned too: a field read only there still names
	// the objects.
	entries, err := os.ReadDir(chart)
	if err != nil {
		t.Fatal(err)
	}
	defines := map[string]string{}
	defineRE := regexp.MustCompile(`\{\{-? define "([^"]+)" -?\}\}`)
	for _, entry := range entries {
		text := read(entry.Name())
		starts := defineRE.FindAllStringSubmatchIndex(text, -1)
		for i, m := range starts {
			end := len(text)
			if i+1 < len(starts) {
				end = starts[i+1][0]
			}
			defines[text[m[2]:m[3]]] = text[m[0]:end]
		}
	}
	nodegroup := read("nodegroup.yaml")
	pending := []string{body(nodegroup, "proxmoxmachinetemplate"), body(nodegroup, "kubevirtmachinetemplate"), read("_talosconfigtemplate.tpl")}
	seen := map[string]bool{}
	includeRE := regexp.MustCompile(`include "([^"]+)"`)
	for len(pending) > 0 {
		text := pending[0]
		pending = pending[1:]
		collect(`\.group\.([A-Za-z]+)`, text)
		collect(`\$group\.([A-Za-z]+)`, text)
		collect(`\.Values\.([A-Za-z_]+)`, text)
		for _, m := range includeRE.FindAllStringSubmatch(text, -1) {
			if helper, ok := defines[m[1]]; ok && !seen[m[1]] {
				seen[m[1]] = true
				pending = append(pending, helper)
			}
		}
	}
	// Read directly too, but compared through Resolve, or set by the platform
	// rather than by the pool.
	for _, resolved := range []string{"talos", "version", "substrate", "_cluster", "kernelModules"} {
		delete(fields, resolved)
	}
	var got []string
	for field := range fields {
		got = append(got, field)
	}
	slices.Sort(got)
	want := slices.Sorted(slices.Values(templateInputs))
	if !slices.Equal(got, want) {
		t.Fatalf("pool fields the templates read = %v, the webhook holds %v", got, want)
	}
}

func TestAdoptionThatAlsoChangesATemplateInputIsDenied(t *testing.T) {
	v := &Validator{Reader: reader(t, wc("talos", "talos"))}
	legacy := map[string]any{"diskSize": "20Gi"}
	// Each value has the field's type, so the denial comes from the input
	// comparison rather than from the type check in Resolve.
	changed := map[string]any{
		"diskSize": "30Gi", "gpus": []any{map[string]any{"name": "nvidia.com/GA102GL_A10"}},
		"instanceType": "u1.large", "kubelet": map[string]any{"maxPods": 200}, "logSerialConsole": true,
		"osImage": map[string]any{"factory": map[string]any{}}, "podCpuLimit": "2", "podCpuRequest": "1",
		"proxmox": map[string]any{"pool": "x"}, "resources": map[string]any{"cpu": "2", "memory": "4Gi"},
		"roles": []any{"ingress-nginx"}, "storageClass": "replicated",
		"labels":  map[string]any{"example.com/team": "a"},
		"taints":  []any{map[string]any{"key": "dedicated", "value": "a", "effect": "NoSchedule"}},
		"cluster": "other", "network": map[string]any{"name": "vpc"}, "routeTable": "to-internet",
	}
	for _, field := range templateInputs {
		value, ok := changed[field]
		if !ok {
			t.Fatalf("no changed value for %s", field)
		}
		adopted := map[string]any{"workerClassName": "talos", "diskSize": "20Gi"}
		adopted[field] = value
		// GPUs also change the resolved Talos configuration, which the
		// equivalence check refuses before the input comparison is reached.
		want := "also changes " + field
		if field == "gpus" {
			want = "changes legacy configuration"
		}
		resp := v.Handle(context.Background(), req(av1.Update, adopted, legacy))
		if resp.Allowed || resp.Result == nil || !strings.Contains(resp.Result.Message, want) {
			t.Fatalf("adoption that also changes %s = %v", field, resp.Result)
		}
	}
	if resp := v.Handle(context.Background(), req(av1.Update, map[string]any{"workerClassName": "talos", "diskSize": "20Gi"}, legacy)); !resp.Allowed {
		t.Fatalf("class-only adoption denied: %v", resp.Result)
	}
	resized := map[string]any{"diskSize": "30Gi"}
	if resp := v.Handle(context.Background(), req(av1.Update, map[string]any{"workerClassName": "talos", "diskSize": "30Gi"}, resized)); !resp.Allowed {
		t.Fatalf("class-only adoption after the disk change reconciled denied: %v", resp.Result)
	}
}
