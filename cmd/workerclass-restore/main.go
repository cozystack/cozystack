package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"

	workers "github.com/cozystack/cozystack/api/workers/v1alpha1"
	"github.com/cozystack/cozystack/internal/kubernetesworker/config"
	"github.com/cozystack/cozystack/internal/kubernetesworker/restore"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	ext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	flags := flag.NewFlagSet("workerclass-restore", flag.ContinueOnError)
	var kubeconfig, kubecontext, namespace, release, currentChart, oldChart string
	var apply bool
	flags.StringVar(&kubeconfig, "kubeconfig", "", "Explicit hoster kubeconfig.")
	flags.StringVar(&kubecontext, "context", "", "Explicit context in that kubeconfig.")
	flags.StringVar(&namespace, "namespace", "", "Pool HelmRelease namespace.")
	flags.StringVar(&release, "release", "", "Pool HelmRelease name.")
	flags.StringVar(&currentChart, "current-chart", "", "Extracted chart currently rendering the classified pool.")
	flags.StringVar(&oldChart, "old-chart", "", "Extracted pre-class chart targeted by rollback.")
	flags.BoolVar(&apply, "apply", false, "Write restored values only after both server-backed renders agree.")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if kubeconfig == "" || kubecontext == "" || namespace == "" || release == "" || currentChart == "" || oldChart == "" {
		return fmt.Errorf("kubeconfig, context, namespace, release, current-chart and old-chart are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}, &clientcmd.ConfigOverrides{CurrentContext: kubecontext})
	cfg, err := loader.ClientConfig()
	if err != nil {
		return err
	}
	scheme := runtime.NewScheme()
	if err = workers.AddToScheme(scheme); err != nil {
		return err
	}
	if err = corev1.AddToScheme(scheme); err != nil {
		return err
	}
	if err = helmv2.AddToScheme(scheme); err != nil {
		return err
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	hr := &helmv2.HelmRelease{}
	if err = c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: release}, hr); err != nil {
		return err
	}
	if hr.Spec.Values == nil {
		return fmt.Errorf("pool has no inline values")
	}
	var values map[string]any
	if err = json.Unmarshal(hr.Spec.Values.Raw, &values); err != nil {
		return err
	}
	name, err := config.ClassName(values)
	if err != nil {
		return err
	}
	class := &workers.WorkerClass{}
	if config.Builtin(name) {
		class.Spec.Bootstrap = workers.Bootstrap(name)
	} else {
		if err = c.Get(ctx, types.NamespacedName{Name: name}, class); err != nil {
			return err
		}
	}
	references, err := restore.ReferenceValues(ctx, c, hr)
	if err != nil {
		return err
	}
	if referencedClass, err := config.ClassName(references); err != nil || referencedClass != "" {
		return fmt.Errorf("workerClassName must be inline, not inherited through valuesFrom")
	}
	effective := restore.MergeValues(references, values)
	prepared, err := restore.Prepare(&class.Spec, effective)
	if err != nil {
		return err
	}
	legacy := restore.MergeValues(nil, values)
	for _, field := range []string{"talos", "osImage", "kernelModules", "proxmox"} {
		if value, exists := prepared[field]; exists {
			legacy[field] = value
		}
	}
	delete(legacy, config.ClassField)
	render := func(chart string, pool map[string]any) ([]byte, error) {
		file, err := os.CreateTemp("", "workerclass-restore-*.json")
		if err != nil {
			return nil, err
		}
		defer func() { _ = os.Remove(file.Name()) }()
		data, err := json.Marshal(pool)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		if _, err = file.Write(data); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err = file.Close(); err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, "helm", "template", release, chart, "--namespace", namespace, "--kubeconfig", kubeconfig, "--kube-context", kubecontext, "--dry-run=server", "--values", file.Name())
		out, err := cmd.Output()
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				return nil, fmt.Errorf("rendering %s: %w: %s", chart, err, exit.Stderr)
			}
			return nil, fmt.Errorf("rendering %s: %w", chart, err)
		}
		return out, nil
	}
	current, err := render(currentChart, restore.MergeValues(references, values))
	if err != nil {
		return err
	}
	restored, err := render(oldChart, restore.MergeValues(references, legacy))
	if err != nil {
		return err
	}
	if err = restore.VerifyRendered(current, restored); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		return err
	}
	if apply {
		hr.Spec.Values = &ext.JSON{Raw: raw}
		if err = c.Update(ctx, hr); err != nil {
			return fmt.Errorf("restoration write failed; re-read and re-prove instead of refreshing resourceVersion: %w", err)
		}
	}
	fmt.Println(string(raw))
	return nil
}
