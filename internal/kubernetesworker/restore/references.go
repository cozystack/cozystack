package restore

import (
	"context"
	"fmt"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// ReferenceValues snapshots map-valued Helm references for both renders.
// targetPath has different strvals semantics and is refused, not approximated.
func ReferenceValues(ctx context.Context, c client.Reader, hr *helmv2.HelmRelease) (map[string]any, error) {
	out := map[string]any{}
	for _, ref := range hr.Spec.ValuesFrom {
		if ref.TargetPath != "" {
			return nil, fmt.Errorf("restore cannot prove valuesFrom targetPath %q; resolve that producer explicitly first", ref.TargetPath)
		}
		key := ref.ValuesKey
		if key == "" {
			key = "values.yaml"
		}
		var data []byte
		var found bool
		var err error
		switch ref.Kind {
		case "Secret":
			obj := &corev1.Secret{}
			err = c.Get(ctx, types.NamespacedName{Namespace: hr.Namespace, Name: ref.Name}, obj)
			data, found = obj.Data[key]
		case "ConfigMap":
			obj := &corev1.ConfigMap{}
			err = c.Get(ctx, types.NamespacedName{Namespace: hr.Namespace, Name: ref.Name}, obj)
			var text string
			text, found = obj.Data[key]
			data = []byte(text)
		default:
			return nil, fmt.Errorf("unsupported valuesFrom kind %q", ref.Kind)
		}
		if err != nil {
			if ref.Optional && apierrors.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("valuesFrom %s/%s has no key %s", ref.Kind, ref.Name, key)
		}
		var values map[string]any
		if err = yaml.Unmarshal(data, &values); err != nil {
			return nil, err
		}
		out = MergeValues(out, values)
	}
	return out, nil
}

func MergeValues(base, overrides map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		if nested, ok := v.(map[string]any); ok {
			out[k] = MergeValues(nested, nil)
		} else {
			out[k] = v
		}
	}
	for k, v := range overrides {
		nested, ok := v.(map[string]any)
		old, wasMap := out[k].(map[string]any)
		if ok && wasMap {
			out[k] = MergeValues(old, nested)
		} else {
			out[k] = v
		}
	}
	return out
}
