package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/chartutil"
	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The Flux helper requires client.Client but only uses Get. Delegating that
// operation to the API reader avoids starting Secret/ConfigMap informers.
type valuesReader struct {
	client.Client
	reader client.Reader
}

func (r valuesReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return r.reader.Get(ctx, key, obj, opts...)
}

func (r *Reconciler) effectiveClass(ctx context.Context, hr *helmv2.HelmRelease) (string, error) {
	inline := map[string]any{}
	if hr.Spec.Values != nil {
		if err := json.Unmarshal(hr.Spec.Values.Raw, &inline); err != nil {
			return "", errors.New("invalid HelmRelease values: want an object")
		}
	}
	// Parser errors can contain referenced data, including sibling assignment keys.
	values, err := chartutil.ChartValuesFromReferences(ctx, logr.Discard(), valuesReader{reader: r.Reader}, hr.Namespace, inline, hr.Spec.ValuesFrom...)
	if err != nil {
		// The API reason, not the message: a status message can quote the object.
		reason := apierrors.ReasonForError(err)
		if reason == metav1.StatusReasonUnknown {
			reason = "not an API status"
		}
		return "", fmt.Errorf("cannot resolve workerClassName values references (%s); check namespace grants and referenced values", reason)
	}
	value, present := values["workerClassName"]
	if !present {
		return "", nil
	}
	name, ok := value.(string)
	if !ok {
		return "", errors.New("workerClassName must be a string")
	}
	return name, nil
}
