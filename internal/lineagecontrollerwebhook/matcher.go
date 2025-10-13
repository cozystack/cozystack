package lineagecontrollerwebhook

import (
	"bytes"
	"context"
	"text/template"

	cozyv1alpha1 "github.com/cozystack/cozystack/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// matchName checks if the provided name matches any of the resource names in the array.
// Each entry in resourceNames is treated as a Go template that gets rendered using the passed context.
// A nil resourceNames array matches any string.
func matchName(ctx context.Context, name string, templateContext map[string]string, resourceNames []string) bool {
	if resourceNames == nil {
		return true
	}

	logger := log.FromContext(ctx)
	for _, templateStr := range resourceNames {
		tmpl, err := template.New("resourceName").Parse(templateStr)
		if err != nil {
			logger.Error(err, "failed to parse resource name template", "template", templateStr)
			continue
		}

		var buf bytes.Buffer
		err = tmpl.Execute(&buf, templateContext)
		if err != nil {
			logger.Error(err, "failed to execute resource name template", "template", templateStr, "context", templateContext)
			continue
		}

		if buf.String() == name {
			return true
		}
	}

	return false
}

func matchResourceToSelector(ctx context.Context, name string, templateContext, l map[string]string, s *cozyv1alpha1.CozystackResourceDefinitionResourceSelector) bool {
	sel, err := metav1.LabelSelectorAsSelector(&s.LabelSelector)
	if err != nil {
		log.FromContext(ctx).Error(err, "failed to convert label selector to selector")
		return false
	}
	labelMatches := sel.Matches(labels.Set(l))
	nameMatches := matchName(ctx, name, templateContext, s.ResourceNames)
	return labelMatches && nameMatches
}

func matchResourceToSelectorArray(ctx context.Context, name string, templateContext, l map[string]string, ss []*cozyv1alpha1.CozystackResourceDefinitionResourceSelector) bool {
	for _, s := range ss {
		if matchResourceToSelector(ctx, name, templateContext, l, s) {
			return true
		}
	}
	return false
}

func matchResourceToExcludeInclude(ctx context.Context, name string, templateContext, l map[string]string, resources *cozyv1alpha1.CozystackResourceDefinitionResources) bool {
	if resources == nil {
		return false
	}
	if matchResourceToSelectorArray(ctx, name, templateContext, l, resources.Exclude) {
		return false
	}
	return matchResourceToSelectorArray(ctx, name, templateContext, l, resources.Include)
}
