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

package dbautoscaler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// namespaceMonitoringLabel names the namespace whose vmselect serves a
	// tenant's metrics. Mirrors the WorkloadMonitor controller convention.
	namespaceMonitoringLabel = "namespace.cozystack.io/monitoring"
	vmSelectService          = "vmselect-shortterm"
	vmSelectPort             = "8481"
	vmSelectPath             = "/select/0/prometheus"
)

// vmQueryTimeout bounds a single instant query.
const vmQueryTimeout = 10 * time.Second

// VMClient runs instant PromQL queries against a vmselect Prometheus API. The
// base URL is passed per call so tests can substitute an httptest server.
type VMClient struct {
	HTTP *http.Client
}

// NewVMClient returns a client with a bounded HTTP timeout.
func NewVMClient() *VMClient {
	return &VMClient{HTTP: &http.Client{Timeout: vmQueryTimeout}}
}

// ResolveVMSelectURL builds the vmselect Prometheus base URL for a monitoring
// namespace, e.g. http://vmselect-shortterm.tenant-root.svc:8481/select/0/prometheus
func ResolveVMSelectURL(monitoringNamespace string) string {
	return fmt.Sprintf("http://%s.%s.svc:%s%s", vmSelectService, monitoringNamespace, vmSelectPort, vmSelectPath)
}

// promResponse is the subset of the Prometheus instant-query response we read.
type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string  `json:"metric"`
			Value  [2]json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// QueryScalar runs an instant query and returns the first sample's value. ok is
// false when the endpoint is unreachable, the response is not "success", or the
// result set is empty — the caller must treat !ok as "metric unavailable" and
// never scale blind. An error is returned only for a genuinely malformed
// response; transport failures degrade to (0, false, nil) like the WorkloadMonitor
// controller, so a single vmselect hiccup freezes rather than crashes the loop.
func (c *VMClient) QueryScalar(ctx context.Context, baseURL, query string) (float64, bool, error) {
	if baseURL == "" || query == "" {
		return 0, false, nil
	}
	u, err := url.Parse(strings.TrimRight(baseURL, "/") + "/api/v1/query")
	if err != nil {
		return 0, false, nil
	}
	u.RawQuery = url.Values{"query": {query}}.Encode()

	httpCtx, cancel := context.WithTimeout(ctx, vmQueryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(httpCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, false, nil
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, false, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return 0, false, nil
	}

	var pr promResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return 0, false, fmt.Errorf("decode vmselect response: %w", err)
	}
	if pr.Status != "success" || len(pr.Data.Result) == 0 {
		return 0, false, nil
	}

	// Value is [timestamp, "stringValue"]; the value is a JSON string.
	var raw string
	if err := json.Unmarshal(pr.Data.Result[0].Value[1], &raw); err != nil {
		return 0, false, fmt.Errorf("decode metric value: %w", err)
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false, fmt.Errorf("parse metric value %q: %w", raw, err)
	}
	return f, true, nil
}
