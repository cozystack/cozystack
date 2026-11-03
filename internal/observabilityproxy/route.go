/*
Copyright 2026 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package observabilityproxy

import (
	"fmt"
	"net/http"
	"strings"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// RouteKind is one of the four Kubernetes-shaped roots the proxy serves.
type RouteKind int

const (
	NamespaceLogs RouteKind = iota + 1
	NamespaceMetrics
	ApplicationLog
	ApplicationMetrics
)

func (k RouteKind) String() string {
	switch k {
	case NamespaceLogs:
		return "namespace-logs"
	case NamespaceMetrics:
		return "namespace-metrics"
	case ApplicationLog:
		return "application-log"
	case ApplicationMetrics:
		return "application-metrics"
	}
	return "unknown"
}

const (
	coreGroup = "cozystack.io"
	appsGroup = "apps.cozystack.io"
)

// Route is a request path split into the scope it names and the upstream
// endpoint below the root. Plural and Name are set only on application roots.
type Route struct {
	Kind      RouteKind
	Namespace string
	Plural    string
	Name      string
	Endpoint  string
}

// HTTPError carries the status code the proxy answers with.
type HTTPError struct {
	Code    int
	Message string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("%d: %s", e.Code, e.Message) }

func errorf(code int, format string, args ...any) *HTTPError {
	return &HTTPError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ParseRoute takes the escaped request path. The console nginx matches its
// location on the normalized path but forwards the original one, so a path
// that a normalizer would rewrite (dot segments, empty segments, any
// percent-encoding such as %2F or %2e) is refused instead of interpreted:
// the namespace the SubjectAccessReview names must be the one queried.
func ParseRoute(escapedPath string) (Route, *HTTPError) {
	notFound := errorf(http.StatusNotFound, "not found")
	if strings.Contains(escapedPath, "%") {
		return Route{}, errorf(http.StatusBadRequest, "percent-encoded paths are not accepted")
	}
	segs := strings.Split(strings.TrimPrefix(escapedPath, "/"), "/")
	if len(segs) > 0 && segs[len(segs)-1] == "" {
		segs = segs[:len(segs)-1]
	}
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return Route{}, errorf(http.StatusBadRequest, "non-canonical path")
		}
	}
	if len(segs) < 6 || segs[0] != "apis" || segs[2] != "v1alpha1" || segs[3] != "namespaces" {
		return Route{}, notFound
	}

	var r Route
	var rest []string
	switch {
	case segs[1] == coreGroup && segs[5] == "logs":
		r.Kind, rest = NamespaceLogs, segs[6:]
	case segs[1] == coreGroup && segs[5] == "metrics":
		r.Kind, rest = NamespaceMetrics, segs[6:]
	case segs[1] == appsGroup && len(segs) >= 8 && segs[7] == "log":
		r.Kind, rest = ApplicationLog, segs[8:]
	case segs[1] == appsGroup && len(segs) >= 8 && segs[7] == "metrics":
		r.Kind, rest = ApplicationMetrics, segs[8:]
	default:
		return Route{}, notFound
	}

	r.Namespace = segs[4]
	if errs := validation.IsDNS1123Label(r.Namespace); len(errs) > 0 {
		return Route{}, errorf(http.StatusBadRequest, "invalid namespace %q: %s", r.Namespace, strings.Join(errs, "; "))
	}
	if r.Kind == ApplicationLog || r.Kind == ApplicationMetrics {
		r.Plural, r.Name = segs[5], segs[6]
		if errs := validation.IsDNS1123Label(r.Plural); len(errs) > 0 {
			return Route{}, errorf(http.StatusBadRequest, "invalid resource %q: %s", r.Plural, strings.Join(errs, "; "))
		}
		if errs := validation.IsDNS1123Subdomain(r.Name); len(errs) > 0 {
			return Route{}, errorf(http.StatusBadRequest, "invalid name %q: %s", r.Name, strings.Join(errs, "; "))
		}
	}
	r.Endpoint = strings.Join(rest, "/")
	return r, nil
}

// ResourceAttributes is the single SubjectAccessReview a route requires. It
// depends on the path only, so a POST query authorizes exactly like a GET.
func (r Route) ResourceAttributes() authorizationv1.ResourceAttributes {
	switch r.Kind {
	case NamespaceLogs:
		return authorizationv1.ResourceAttributes{Verb: "list", Group: coreGroup, Resource: "logs", Namespace: r.Namespace}
	case NamespaceMetrics:
		return authorizationv1.ResourceAttributes{Verb: "list", Group: coreGroup, Resource: "metrics", Namespace: r.Namespace}
	case ApplicationLog:
		return authorizationv1.ResourceAttributes{Verb: "get", Group: appsGroup, Resource: r.Plural, Subresource: "log", Name: r.Name, Namespace: r.Namespace}
	case ApplicationMetrics:
		return authorizationv1.ResourceAttributes{Verb: "get", Group: appsGroup, Resource: r.Plural, Subresource: "metrics", Name: r.Name, Namespace: r.Namespace}
	}
	return authorizationv1.ResourceAttributes{}
}
