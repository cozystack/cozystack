package template

import (
	"bytes"
	"encoding/json"
	tmpl "text/template"
)

func Template[T any](obj *T, templateContext map[string]any) (*T, error) {
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	var unstructured any
	err = json.Unmarshal(b, &unstructured)
	if err != nil {
		return nil, err
	}
	templateFunc := func(in string) string {
		out, err := template(in, templateContext)
		if err != nil {
			return in
		}
		return out
	}
	unstructured = mapAtStrings(unstructured, templateFunc)
	b, err = json.Marshal(unstructured)
	if err != nil {
		return nil, err
	}
	var out T
	err = json.Unmarshal(b, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func mapAtStrings(v any, f func(string) string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			x[k] = mapAtStrings(val, f)
		}
		return x
	case []any:
		for i, val := range x {
			x[i] = mapAtStrings(val, f)
		}
		return x
	case string:
		return f(x)
	default:
		return v
	}
}

func template(in string, templateContext map[string]any) (string, error) {
	tpl, err := tmpl.New("this").Parse(in)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, templateContext); err != nil {
		return "", err
	}
	return buf.String(), nil
}
