package lineagecontrollerwebhook

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func matchLabelsToSelector(l map[string]string, s *metav1.LabelSelector) bool {
	// TODO: emit warning if error
	sel, err := metav1.LabelSelectorAsSelector(s)
	if err != nil {
		return false
	}
	return sel.Matches(labels.Set(l))
}

func matchLabelsToSelectorArray(l map[string]string, ss []*metav1.LabelSelector) bool {
	for _, s := range ss {
		if matchLabelsToSelector(l, s) {
			return true
		}
	}
	return false
}

func matchLabelsToExcludeInclude(l map[string]string, ex, in []*metav1.LabelSelector) bool {
	if matchLabelsToSelectorArray(l, ex) {
		return false
	}
	if matchLabelsToSelectorArray(l, in) {
		return true
	}
	return false
}
