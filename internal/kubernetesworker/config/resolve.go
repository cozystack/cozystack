package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"

	workers "github.com/cozystack/cozystack/api/workers/v1alpha1"
)

//go:embed defaults.json
var defaultsJSON []byte

const ClassField = "workerClassName"

var (
	fullVersion  = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9a-z.]+)?$`)
	minorVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+$`)
	schematic    = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	factoryURL   = regexp.MustCompile(`^https?://[A-Za-z0-9._~:/?#\[\]@!&'()*+,;=%-]+$`)
	talosMinor   = regexp.MustCompile(`^v[0-9]+\.[0-9]+`)
	moduleName   = regexp.MustCompile(`^[a-z0-9_-]+$`)
	moduleParam  = regexp.MustCompile("^[^$`\\\\'\"]+$")
	graphic      = regexp.MustCompile(`^[[:graph:]]+$`)
	talosTag     = regexp.MustCompile(`^talos-v[0-9]+\.[0-9]+\.[0-9]+`)
)

type Resolved struct {
	Bootstrap         workers.Bootstrap
	Talos             map[string]any
	OSImage           map[string]any
	KernelModules     []workers.KernelModule
	TemplateTags      []string
	Source            string
	TalosVersion      string
	SchematicID       string
	FactoryURL        string
	KubernetesVersion string
}

type policyDefaults struct {
	KubernetesVersion      string              `json:"kubernetesVersion"`
	Talos                  map[string]any      `json:"talos"`
	TalosKubernetesSupport map[string][]string `json:"talosKubernetesSupport"`
	// LegacyPools holds each set of defaults the pre-WorkerClass schema wrote
	// into a pool; they are frozen, unlike the policy defaults above. The
	// current schema stores none, so a set is added only when a release still
	// on the old schema changes its defaults.
	LegacyPools []map[string]any `json:"legacyPools"`
}

// storedDefault reports whether a classed pool's value is only a legacy
// default the API stored, which means "not set" there and lets the class
// value through. A pool without a class keeps its stored values.
func (d policyDefaults) storedDefault(class *workers.WorkerClassSpec, value any, path ...string) bool {
	if class == nil {
		return false
	}
	for _, legacy := range d.LegacyPools {
		if stored, ok := lookup(legacy, path); ok && reflect.DeepEqual(value, stored) {
			return true
		}
	}
	return false
}

func lookup(m map[string]any, path []string) (any, bool) {
	var v any = m
	for _, key := range path {
		next, ok := asMap(v)[key]
		if !ok {
			return nil, false
		}
		v = next
	}
	return v, true
}

func Builtin(name string) bool { return name == "talos" || name == "kairos" }

func ClassName(pool map[string]any) (string, error) {
	v, ok := pool[ClassField]
	if !ok {
		return "", nil
	}
	// The values schema refuses it, so a null cannot mean "keep the class".
	if v == nil {
		return "", fmt.Errorf("%s: got null, want string", ClassField)
	}
	name, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", ClassField)
	}
	return name, nil
}

func Resolve(class *workers.WorkerClassSpec, pool map[string]any) (*Resolved, error) {
	if err := validatePoolScalarTypes(pool); err != nil {
		return nil, err
	}
	// Decoded per call: out.Talos starts as this map and is then written to.
	var defaults policyDefaults
	if err := json.Unmarshal(defaultsJSON, &defaults); err != nil {
		return nil, err
	}
	out := &Resolved{Bootstrap: workers.BootstrapTalos, Talos: defaults.Talos, OSImage: map[string]any{}, KernelModules: []workers.KernelModule{}}
	if class != nil {
		out.Bootstrap = class.Bootstrap
	}
	version, ok := pool["version"].(string)
	if !ok {
		version = defaults.KubernetesVersion
	}
	if !minorVersion.MatchString(version) {
		return nil, fmt.Errorf("version %q must be a Kubernetes minor", version)
	}
	out.KubernetesVersion = version
	keys, err := stringList(pool["sshKeys"])
	if err != nil {
		return nil, fmt.Errorf("sshKeys: %w", err)
	}
	for _, key := range keys {
		// The key lands in cloud-config, where a line break starts a new directive.
		if key == "" || strings.ContainsAny(key, "\r\n") {
			return nil, fmt.Errorf("sshKeys: a key must be a single non-empty line")
		}
	}
	if out.Bootstrap == workers.BootstrapKairos {
		return kairos(class, pool, out, keys)
	}
	if out.Bootstrap != workers.BootstrapTalos {
		return nil, fmt.Errorf("unsupported bootstrap %q", out.Bootstrap)
	}
	if len(keys) > 0 {
		return nil, fmt.Errorf("sshKeys: Talos has no SSH server")
	}
	if class != nil && class.Talos != nil {
		data, err := json.Marshal(class.Talos)
		if err != nil {
			return nil, err
		}
		var baseline map[string]any
		if err = json.Unmarshal(data, &baseline); err != nil {
			return nil, err
		}
		maps.Copy(out.Talos, baseline)
	}
	for key, value := range asMap(pool["talos"]) {
		// A null member is not set: the typed Go client sends
		// registryMirrors as null, and Helm drops it against the chart
		// default instead of applying it.
		if value == nil {
			continue
		}
		if !defaults.storedDefault(class, value, "talos", key) {
			out.Talos[key] = value
		}
	}
	if err = resolveModules(class, pool, out); err != nil {
		return nil, err
	}
	substrate := "kubevirt"
	if v, ok := pool["substrate"].(string); ok && v != "" {
		substrate = v
	}
	if substrate == "proxmox" {
		return proxmox(class, pool, out, defaults)
	}
	if substrate != "kubevirt" {
		return nil, fmt.Errorf("unsupported substrate %q", substrate)
	}
	if class != nil && class.Substrates != nil && class.Substrates.KubeVirt != nil && class.Substrates.KubeVirt.OSImage != nil {
		data, err := json.Marshal(class.Substrates.KubeVirt.OSImage)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &out.OSImage); err != nil {
			return nil, err
		}
	}
	// A null osImage is not set: a pool stored with one before WorkerClass
	// rendered, since Helm drops it against the chart default.
	if v, present := pool["osImage"]; present && v != nil && !defaults.storedDefault(class, v, "osImage") {
		out.OSImage, _ = v.(map[string]any)
	}
	_, builtin := out.OSImage["builtin"]
	_, factory := out.OSImage["factory"]
	if builtin && factory {
		return nil, fmt.Errorf("osImage: set only one of osImage.builtin or osImage.factory")
	}
	out.Source = "factory"
	selection := map[string]any{}
	if builtin {
		out.Source = "builtin"
		selection = asMap(out.OSImage["builtin"])
	} else if factory {
		selection = asMap(out.OSImage["factory"])
	}
	out.TalosVersion = fallback(selection["version"], out.Talos["version"])
	out.SchematicID = fallback(selection["schematicID"], out.Talos["schematicID"])
	out.FactoryURL = fallback(selection["imageFactoryURL"], out.Talos["imageFactoryURL"])
	if err = validateTalos(out, defaults, origin(class, pool, defaults, "osImage."+out.Source, selection)); err != nil {
		return nil, err
	}
	if class != nil && class.Substrates != nil && class.Substrates.KubeVirt != nil && class.Substrates.KubeVirt.OSImage != nil {
		if fixed := class.Substrates.KubeVirt.OSImage.Builtin; fixed != nil && (out.Source != "builtin" || out.TalosVersion != fixed.Version || out.SchematicID != fixed.SchematicID) {
			return nil, fmt.Errorf("osImage conflicts with the fixed builtin image/version/schematic contract")
		}
	}
	return out, nil
}

func kairos(class *workers.WorkerClassSpec, pool map[string]any, out *Resolved, keys []string) (*Resolved, error) {
	for _, name := range []string{"talos", "kernelModules", "osImage"} {
		if nonempty(pool[name]) && (name != "talos" || !allEmpty(pool[name])) {
			return nil, fmt.Errorf("%s is Talos-specific and unsupported on Kairos", name)
		}
	}
	if nonempty(pool["gpus"]) {
		return nil, fmt.Errorf("gpus: Kairos workers have no NVIDIA driver path; use a Talos class for a GPU pool")
	}
	if substrate, ok := pool["substrate"].(string); ok && substrate != "" && substrate != "kubevirt" {
		return nil, fmt.Errorf("bootstrap kairos requires substrate kubevirt")
	}
	if len(keys) > 1 {
		return nil, fmt.Errorf("sshKeys: the Kairos bootstrap provider authorizes a single key")
	}
	if class != nil && class.Image != nil {
		if !strings.HasPrefix(class.Image.KubernetesVersion, out.KubernetesVersion+".") {
			return nil, fmt.Errorf("image Kubernetes minor differs from the pool's version %s", out.KubernetesVersion)
		}
		out.KubernetesVersion = class.Image.KubernetesVersion
	}
	out.Source = "kairos"
	out.Talos = nil
	return out, nil
}

func resolveModules(class *workers.WorkerClassSpec, pool map[string]any, out *Resolved) error {
	if class != nil && class.KernelModules != nil {
		out.KernelModules = slices.Clone(*class.KernelModules)
	}
	if raw, present := pool["kernelModules"]; present {
		data, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		// A fresh slice: decoding into the class's copy would keep its fields in
		// elements the pool list does not set.
		var modules []workers.KernelModule
		if err = json.Unmarshal(data, &modules); err != nil {
			return fmt.Errorf("kernelModules: %w", err)
		}
		out.KernelModules = modules
		return validateModules(out.KernelModules)
	}
	if class != nil && class.KernelModules != nil {
		return validateModules(out.KernelModules)
	}
	gpus, _ := pool["gpus"].([]any)
	for _, raw := range gpus {
		if name, _ := asMap(raw)["name"].(string); strings.HasPrefix(name, "nvidia.com/") {
			for _, m := range []string{"nvidia", "nvidia_uvm", "nvidia_drm", "nvidia_modeset"} {
				module := workers.KernelModule{Name: m}
				if m == "nvidia" {
					module.Parameters = []string{"NVreg_NvLinkDisable=1"}
				}
				out.KernelModules = append(out.KernelModules, module)
			}
			break
		}
	}
	return nil
}

func proxmox(class *workers.WorkerClassSpec, pool map[string]any, out *Resolved, defaults policyDefaults) (*Resolved, error) {
	out.Source = "proxmox"
	out.TalosVersion, _ = out.Talos["version"].(string)
	out.SchematicID, _ = out.Talos["schematicID"].(string)
	if nonempty(pool["osImage"]) {
		return nil, fmt.Errorf("osImage is not a Proxmox source")
	}
	var fixed *workers.ProxmoxSource
	if class != nil && class.Substrates != nil {
		fixed = class.Substrates.Proxmox
		if fixed != nil {
			out.TemplateTags = slices.Clone(fixed.TemplateTags)
		}
	}
	// A null is not set: a pool stored with one before WorkerClass rendered,
	// since Helm drops it against the chart default.
	if raw, present := asMap(pool["proxmox"])["templateTags"]; present && raw != nil && !defaults.storedDefault(class, raw, "proxmox", "templateTags") {
		tags, err := stringList(raw)
		if err != nil {
			return nil, fmt.Errorf("proxmox.templateTags: %w", err)
		}
		out.TemplateTags = tags
	}
	if len(out.TemplateTags) == 0 {
		return nil, fmt.Errorf("proxmox.templateTags must not be empty")
	}
	// The template is the boot disk and the Talos release reaches only the
	// installer, so a tag naming another release would boot the old kernel.
	for _, tag := range out.TemplateTags {
		if talosTag.MatchString(tag) && tag != "talos-"+out.TalosVersion {
			return nil, fmt.Errorf("proxmox.templateTags selects a template tagged %q, but talos.version is %q", tag, out.TalosVersion)
		}
	}
	if fixed != nil {
		want := slices.Sorted(slices.Values(fixed.TemplateTags))
		got := slices.Sorted(slices.Values(out.TemplateTags))
		if !reflect.DeepEqual(got, want) || class.Talos == nil || class.Talos.Version == nil || class.Talos.SchematicID == nil || out.TalosVersion != *class.Talos.Version || out.SchematicID != *class.Talos.SchematicID {
			return nil, fmt.Errorf("proxmox.templateTags or talos version/schematic conflicts with fixed template contract")
		}
	}
	if err := validateTalos(out, defaults, origin(class, pool, defaults, "", nil)); err != nil {
		return nil, err
	}
	return out, nil
}

// PoolError refuses a value the pool sets itself, so admission names the
// pool rather than the class the value overrides.
type PoolError struct{ error }

// originFunc names where value came from: the path of the field that holds
// it, and whether that field is the pool's own setting on a pool with a class.
type originFunc func(key, value string) (path string, pool bool)

// origin reports the selected image member when it supplies value, since it
// wins over talos.<key>; a class's member is named under its substrate. A pool
// without a class has nothing to override, so no value is attributed to it as
// an override.
func origin(class *workers.WorkerClassSpec, pool map[string]any, defaults policyDefaults, selectionPath string, selection map[string]any) originFunc {
	source := strings.TrimPrefix(selectionPath, "osImage.")
	return func(key, value string) (string, bool) {
		if own, _ := selection[key].(string); own != "" && selectionPath != "" {
			if class == nil {
				return selectionPath + "." + key, false
			}
			if raw := asMap(asMap(pool["osImage"])[source]); own == value && raw[key] == own {
				return selectionPath + "." + key, true
			}
			return "substrates.kubevirt.osImage." + source + "." + key, false
		}
		if class != nil {
			raw, present := asMap(pool["talos"])[key]
			if own, ok := raw.(string); present && ok && own == value && !defaults.storedDefault(class, raw, "talos", key) {
				return "talos." + key, true
			}
		}
		return "talos." + key, false
	}
}

// refuse names the field the refused value came from, and wraps the refusal as
// a PoolError when the pool set that value over a class.
func refuse(from originFunc, key, value, format string, args ...any) error {
	path, pool := from(key, value)
	msg := fmt.Sprintf(format, append([]any{path}, args...)...)
	if pool {
		return PoolError{fmt.Errorf("pool: %s", msg)}
	}
	return errors.New(msg)
}

func validateTalos(out *Resolved, defaults policyDefaults, from originFunc) error {
	if !fullVersion.MatchString(out.TalosVersion) {
		if _, pool := from("version", out.TalosVersion); pool {
			return refuse(from, "version", out.TalosVersion, "%s %q must be a full Talos release", out.TalosVersion)
		}
		return refuse(from, "version", out.TalosVersion, "%s %q must be a full release", out.TalosVersion)
	}
	if !schematic.MatchString(out.SchematicID) {
		return refuse(from, "schematicID", out.SchematicID, "%s %q is invalid", out.SchematicID)
	}
	minor := talosMinor.FindString(out.TalosVersion)
	if supported, known := defaults.TalosKubernetesSupport[minor]; known && !slices.Contains(supported, out.KubernetesVersion) {
		return refuse(from, "version", out.TalosVersion, "%s %s does not support Kubernetes %s", out.TalosVersion, out.KubernetesVersion)
	}
	if out.Source == "factory" {
		u, err := url.Parse(out.FactoryURL)
		if err != nil || u.Hostname() == "" || !factoryURL.MatchString(out.FactoryURL) {
			return refuse(from, "imageFactoryURL", out.FactoryURL, "%s %q must be an HTTP(S) URL", out.FactoryURL)
		}
	}
	return nil
}

func validatePoolScalarTypes(pool map[string]any) error {
	if err := validateStringMembers(pool, "", "version", "substrate"); err != nil {
		return err
	}
	for _, name := range []string{"talos", "osImage", "proxmox"} {
		if value, present := pool[name]; present && value != nil {
			if _, ok := value.(map[string]any); !ok {
				return fmt.Errorf("%s must be an object", name)
			}
		}
	}
	// For the null forms checked here Go is no more lenient than the chart's
	// values schema: a null it admitted would leave a pool that does not
	// render. Checked here rather than where a path reads the field, so the
	// paths that ignore the field refuse it too.
	if value, present := pool["kernelModules"]; present && value == nil {
		return fmt.Errorf("kernelModules: got null, want array")
	}
	if value, present := pool["kernelModules"]; present && value != nil {
		kind := reflect.TypeOf(value).Kind()
		if kind != reflect.Slice && kind != reflect.Array {
			return fmt.Errorf("kernelModules must be an array")
		}
	}
	talos := asMap(pool["talos"])
	// Only registryMirrors, sysctls and imageFactoryCA have chart defaults that
	// make Helm drop a null; the schema refuses a null anywhere else in talos.
	for _, name := range []string{"version", "schematicID", "imageFactoryURL", "installerRepository"} {
		if value, present := talos[name]; present && value == nil {
			return fmt.Errorf("talos.%s: got null, want string", name)
		}
	}
	if err := validateStringMembers(talos, "talos.", "version", "schematicID", "imageFactoryURL", "imageFactoryCA", "installerRepository"); err != nil {
		return err
	}
	if mirrors, present := talos["registryMirrors"]; present && mirrors != nil {
		if _, ok := mirrors.(map[string]any); !ok {
			return fmt.Errorf("talos.registryMirrors must be an object")
		}
	}
	image := asMap(pool["osImage"])
	for _, name := range []string{"factory", "builtin"} {
		if value, present := image[name]; present && value == nil {
			return fmt.Errorf("osImage.%s: got null, want object", name)
		}
		if value, present := image[name]; present {
			members, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("osImage.%s must be an object", name)
			}
			names := []string{"version", "schematicID"}
			if name == "factory" {
				names = append(names, "imageFactoryURL")
			}
			for _, member := range names {
				if value, present := members[member]; present && value == nil {
					return fmt.Errorf("osImage.%s.%s: got null, want string", name, member)
				}
			}
			if err := validateStringMembers(members, "osImage."+name+".", names...); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateStringMembers(values map[string]any, prefix string, names ...string) error {
	for _, name := range names {
		if value, present := values[name]; present && value != nil {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("%s%s must be a string", prefix, name)
			}
		}
	}
	return nil
}

func fallback(v, baseline any) string {
	if text, ok := v.(string); ok && text != "" {
		return text
	}
	text, _ := baseline.(string)
	return text
}
func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

// allEmpty reports a map whose every member is empty, as the chart defaults
// leave talos on a pool that sets none of it.
func allEmpty(v any) bool {
	for _, member := range asMap(v) {
		if nonempty(member) {
			return false
		}
	}
	return true
}

func nonempty(v any) bool {
	switch value := v.(type) {
	case nil:
		return false
	case string:
		return value != ""
	case []any:
		return len(value) > 0
	case map[string]any:
		return len(value) > 0
	default:
		return true
	}
}
func stringList(v any) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var list []string
	if err = json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("must be a string list: %w", err)
	}
	return list, nil
}

func validateModules(modules []workers.KernelModule) error {
	for _, m := range modules {
		if !moduleName.MatchString(m.Name) {
			return fmt.Errorf("invalid kernelModules name %q", m.Name)
		}
		for _, p := range m.Parameters {
			if !graphic.MatchString(p) || !moduleParam.MatchString(p) {
				return fmt.Errorf("invalid kernelModules parameter %q", p)
			}
		}
	}
	return nil
}

// Equivalent compares resolved consumers, not the syntactic source selector.
// An absent factory selector and an explicit empty factory render identically.
func Equivalent(a, b *Resolved) bool {
	left, right := *a, *b
	for _, resolved := range []*Resolved{&left, &right} {
		resolved.Talos = maps.Clone(resolved.Talos)
		resolved.TemplateTags = slices.Sorted(slices.Values(resolved.TemplateTags))
		if resolved.Source == "builtin" || resolved.Source == "proxmox" {
			delete(resolved.Talos, "imageFactoryURL")
			delete(resolved.Talos, "imageFactoryCA")
			resolved.FactoryURL = ""
		}
		if resolved.Source == "factory" || resolved.Source == "builtin" {
			resolved.Talos["version"] = resolved.TalosVersion
			resolved.Talos["schematicID"] = resolved.SchematicID
			if resolved.Source == "factory" {
				resolved.Talos["imageFactoryURL"] = resolved.FactoryURL
			}
		}
	}
	left.OSImage = nil
	right.OSImage = nil
	l, le := json.Marshal(left)
	r, re := json.Marshal(right)
	return le == nil && re == nil && bytes.Equal(l, r)
}
