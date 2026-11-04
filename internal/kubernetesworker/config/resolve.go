package config

import (
	_ "embed"
	"encoding/json"
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
	fullVersion  = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$`)
	minorVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+$`)
	schematic    = regexp.MustCompile(`^[a-z0-9]+$`)
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
}

func Builtin(name string) bool { return name == "talos" || name == "kairos" }

func ClassName(pool map[string]any) (string, error) {
	v, ok := pool[ClassField]
	if !ok || v == nil {
		return "", nil
	}
	name, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", ClassField)
	}
	return name, nil
}

func Resolve(class *workers.WorkerClassSpec, pool map[string]any) (*Resolved, error) {
	var defaults policyDefaults
	if err := json.Unmarshal(defaultsJSON, &defaults); err != nil {
		return nil, err
	}
	out := &Resolved{Bootstrap: workers.BootstrapTalos, Talos: defaults.Talos, OSImage: map[string]any{}, KernelModules: []workers.KernelModule{}}
	if class != nil {
		out.Bootstrap = class.Bootstrap
	}
	version, ok := pool["version"].(string)
	if !ok || version == "" {
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
	if v, ok := pool["talos"]; ok && v != nil {
		overrides, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("talos must be an object")
		}
		maps.Copy(out.Talos, overrides)
	}
	if err = resolveModules(class, pool, out); err != nil {
		return nil, err
	}
	substrate := "kubevirt"
	if v, ok := pool["substrate"].(string); ok && v != "" {
		substrate = v
	}
	if substrate == "proxmox" {
		return proxmox(class, pool, out)
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
	if v, present := pool["osImage"]; present {
		source, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("osImage must be an object")
		}
		out.OSImage = source
	}
	_, builtin := out.OSImage["builtin"]
	_, factory := out.OSImage["factory"]
	if builtin && factory {
		return nil, fmt.Errorf("osImage must select only one source")
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
	if err = validateTalos(out, defaults); err != nil {
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
		if nonempty(pool[name]) {
			return nil, fmt.Errorf("%s is Talos-specific and unsupported on Kairos", name)
		}
	}
	if substrate, ok := pool["substrate"].(string); ok && substrate != "" && substrate != "kubevirt" {
		return nil, fmt.Errorf("bootstrap kairos requires substrate kubevirt")
	}
	if len(keys) > 1 {
		return nil, fmt.Errorf("sshKeys: Kairos accepts at most one key")
	}
	if class != nil && class.Image != nil {
		if !strings.HasPrefix(class.Image.KubernetesVersion, out.KubernetesVersion+".") {
			return nil, fmt.Errorf("image Kubernetes minor conflicts with version %s", out.KubernetesVersion)
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
		if raw == nil {
			return fmt.Errorf("kernelModules must be a list, not null")
		}
		data, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(data, &out.KernelModules); err != nil {
			return fmt.Errorf("kernelModules: %w", err)
		}
		return nil
	}
	if class != nil && class.KernelModules != nil {
		return nil
	}
	gpus, _ := pool["gpus"].([]any)
	for _, raw := range gpus {
		if name, _ := asMap(raw)["name"].(string); strings.HasPrefix(name, "nvidia.com/") {
			for _, m := range []string{"nvidia", "nvidia_uvm", "nvidia_drm", "nvidia_modeset"} {
				out.KernelModules = append(out.KernelModules, workers.KernelModule{Name: m})
			}
			break
		}
	}
	return nil
}

func proxmox(class *workers.WorkerClassSpec, pool map[string]any, out *Resolved) (*Resolved, error) {
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
	if raw, present := asMap(pool["proxmox"])["templateTags"]; present {
		tags, err := stringList(raw)
		if err != nil {
			return nil, fmt.Errorf("proxmox.templateTags: %w", err)
		}
		out.TemplateTags = tags
	}
	if len(out.TemplateTags) == 0 {
		return nil, fmt.Errorf("proxmox.templateTags must not be empty")
	}
	if fixed != nil {
		want := slices.Sorted(slices.Values(fixed.TemplateTags))
		got := slices.Sorted(slices.Values(out.TemplateTags))
		if !reflect.DeepEqual(got, want) || class.Talos == nil || class.Talos.Version == nil || class.Talos.SchematicID == nil || out.TalosVersion != *class.Talos.Version || out.SchematicID != *class.Talos.SchematicID {
			return nil, fmt.Errorf("proxmox.templateTags or talos version/schematic conflicts with fixed template contract")
		}
	}
	var defaults policyDefaults
	if err := json.Unmarshal(defaultsJSON, &defaults); err != nil {
		return nil, err
	}
	if err := validateTalos(out, defaults); err != nil {
		return nil, err
	}
	return out, nil
}

func validateTalos(out *Resolved, defaults policyDefaults) error {
	if !fullVersion.MatchString(out.TalosVersion) {
		return fmt.Errorf("talos.version %q must be a full release", out.TalosVersion)
	}
	if !schematic.MatchString(out.SchematicID) {
		return fmt.Errorf("talos.schematicID %q is invalid", out.SchematicID)
	}
	minor := regexp.MustCompile(`^v[0-9]+\.[0-9]+`).FindString(out.TalosVersion)
	if supported, known := defaults.TalosKubernetesSupport[minor]; known && !slices.Contains(supported, out.KubernetesVersion) {
		return fmt.Errorf("talos.version %s does not support Kubernetes %s", out.TalosVersion, out.KubernetesVersion)
	}
	if out.Source == "factory" {
		u, err := url.Parse(out.FactoryURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("talos.imageFactoryURL %q must be an HTTP(S) URL", out.FactoryURL)
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
