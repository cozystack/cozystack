//go:build ignore

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cozystack/cozystack/internal/kubernetesworker/config"
	appsv1alpha1 "github.com/cozystack/cozystack/pkg/apis/apps/v1alpha1"
	"sigs.k8s.io/yaml"
)

func main() {
	input := flag.String("input", "", "Generated KubernetesNodes ApplicationDefinition.")
	goOutput := flag.String("go-output", "", "Embedded resource descriptor.")
	chartOutput := flag.String("chart-output", "", "Package chart resource descriptor.")
	flag.Parse()
	if err := generate(*input, *goOutput, *chartOutput); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func generate(input, goOutput, chartOutput string) error {
	raw, err := os.ReadFile(input)
	if err != nil {
		return err
	}
	var definition struct {
		Spec struct{ Application struct{ Kind, Plural string } }
	}
	if err = yaml.Unmarshal(raw, &definition); err != nil {
		return err
	}
	if definition.Spec.Application.Kind != "KubernetesNodes" || definition.Spec.Application.Plural == "" {
		return fmt.Errorf("input must describe the actual KubernetesNodes resource")
	}
	descriptor := config.ResourceDescriptor{Group: appsv1alpha1.SchemeGroupVersion.Group, Version: appsv1alpha1.SchemeGroupVersion.Version, Kind: definition.Spec.Application.Kind, Resource: definition.Spec.Application.Plural}
	data, err := json.MarshalIndent(descriptor, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	for _, output := range []string{goOutput, chartOutput} {
		if output == "" {
			return fmt.Errorf("both output paths are required")
		}
		if err = os.MkdirAll(filepath.Dir(output), 0755); err != nil {
			return err
		}
		if err = os.WriteFile(output, data, 0644); err != nil {
			return err
		}
	}
	return nil
}
