// SPDX-License-Identifier: Apache-2.0

package migrationcontroller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The controller reads the source's topology from Forklift's inventory service
// rather than from Forklift's custom resources, because the resources do not
// carry it.
//
// The tempting shortcut is to let Forklift tell you what is missing: a Plan
// whose maps do not cover a VM publishes VMNetworksNotMapped/VMStorageNotMapped
// with the offenders in `items`. But those items are the *VM* references, not
// the networks or datastores — `pkg/controller/plan/validation.go:940,960`
// appends `ref.String()` of the VM, rendered `id:vm-1234 name:'web-01'`
// (`pkg/apis/forklift/v1beta1/ref/ref.go:29` at v2.11.5). The network and
// datastore IDs a map entry needs appear on no Forklift CR at all, and map
// resolution is an exact-ID lookup with no wildcard to fall back on
// (`adapter/vsphere/validator.go:80,116`). A map built by parsing that text
// resolves to nothing and the task waits in Validating forever.
//
// So this is a real HTTP client with a bearer token. It stays inside the "no
// second vSphere client" boundary: it never talks to vCenter, only to Forklift,
// and what it reads is Forklift's own view of the source.
const (
	// inventoryTokenFile is the projected ServiceAccount token the inventory
	// service authenticates. Re-read per call: it rotates.
	inventoryTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token" // #nosec G101 -- path, not a credential

	// inventoryCASecret holds the CA that signed the inventory's serving
	// certificate. Verifying against it is what lets this client run without
	// InsecureSkipVerify: the certificate carries the service DNS names as SANs.
	inventoryCASecret = "forklift-inventory-serving-cert"
	inventoryCAKey    = "ca.crt"
)

// inventoryRef is Forklift's reference shape: a kind and the provider's own ID.
type inventoryRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// inventoryVM is the subset of a vSphere VM record this controller consumes.
type inventoryVM struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Firmware   string         `json:"firmware"`
	PowerState string         `json:"powerState"`
	Networks   []inventoryRef `json:"networks"`
	Disks      []struct {
		Datastore inventoryRef `json:"datastore"`
	} `json:"disks"`
}

// datastoreIDs returns the distinct datastores backing the VM's disks.
func (v *inventoryVM) datastoreIDs() []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range v.Disks {
		if d.Datastore.ID == "" || seen[d.Datastore.ID] {
			continue
		}
		seen[d.Datastore.ID] = true
		out = append(out, d.Datastore.ID)
	}
	return out
}

// networkIDs returns the distinct networks the VM's interfaces attach to.
func (v *inventoryVM) networkIDs() []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range v.Networks {
		if n.ID == "" || seen[n.ID] {
			continue
		}
		seen[n.ID] = true
		out = append(out, n.ID)
	}
	return out
}

// NewInventoryClient builds the client the task reconciler reads topology with.
// The reader must be uncached — the CA secret lives in Forklift's namespace,
// which this controller does not watch.
func NewInventoryClient(baseURL, namespace string, reader client.Reader) *inventoryClient {
	return &inventoryClient{BaseURL: baseURL, Namespace: namespace, Reader: reader}
}

// inventoryClient reads VM records from Forklift's inventory REST service.
type inventoryClient struct {
	// BaseURL is the inventory service root, e.g.
	// https://forklift-inventory.cozy-forklift.svc:8443
	BaseURL string
	// Namespace is where the CA secret lives — the Forklift namespace.
	Namespace string
	// Reader fetches the CA secret. Nil disables verification setup and makes
	// every call fail, which is the safe default for a misconfigured client.
	Reader client.Reader
	// TokenFile overrides the projected token path in tests.
	TokenFile string

	mu   sync.Mutex
	http *http.Client
}

// notFoundError marks a VM the provider's inventory does not have, so the
// caller can report it as a tenant-visible "no such VM" rather than retrying.
type notFoundError struct{ vmID string }

func (e *notFoundError) Error() string {
	return fmt.Sprintf("VM %q is not present in the source inventory", e.vmID)
}

// inventoryVMRef is the abbreviated record the list endpoint returns: enough to
// name a machine in a picker, without paying for the full record of every VM in
// the source.
type inventoryVMRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Path is the VM's inventory path, which is what distinguishes two machines
	// that share a name in different folders.
	Path string `json:"path"`
}

// VMs lists every VM the provider's inventory holds.
func (c *inventoryClient) VMs(ctx context.Context, providerUID string) ([]inventoryVMRef, error) {
	resp, err := c.get(ctx, fmt.Sprintf("%s/providers/vsphere/%s/vms", c.BaseURL, providerUID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Forklift inventory returned %s listing VMs", resp.Status)
	}

	var vms []inventoryVMRef
	if err := json.NewDecoder(resp.Body).Decode(&vms); err != nil {
		return nil, fmt.Errorf("decoding the inventory VM list: %w", err)
	}
	return vms, nil
}

// get issues an authenticated GET. The token is read per call because it
// rotates, and the caller owns the response body.
func (c *inventoryClient) get(ctx context.Context, url string) (*http.Response, error) {
	httpClient, err := c.client(ctx)
	if err != nil {
		return nil, err
	}
	token, err := c.token()
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("querying Forklift inventory: %w", err)
	}
	return resp, nil
}

// VM fetches one VM record by the provider's UID and the VM's managed-object ID.
func (c *inventoryClient) VM(ctx context.Context, providerUID, vmID string) (*inventoryVM, error) {
	resp, err := c.get(ctx, fmt.Sprintf("%s/providers/vsphere/%s/vms/%s", c.BaseURL, providerUID, vmID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, &notFoundError{vmID: vmID}
	default:
		return nil, fmt.Errorf("Forklift inventory returned %s for VM %q", resp.Status, vmID)
	}

	vm := &inventoryVM{}
	if err := json.NewDecoder(resp.Body).Decode(vm); err != nil {
		return nil, fmt.Errorf("decoding the inventory record for VM %q: %w", vmID, err)
	}
	return vm, nil
}

// client builds the HTTPS client, trusting the CA that signed the inventory's
// serving certificate. Built once and reused.
//
// Guarded because one client is shared by both reconcilers, which run on
// separate goroutines: an unsynchronized check-then-assign races on the first
// call, leaking the loser's client and failing under -race. A mutex rather than
// sync.Once deliberately — construction reads a Secret and can fail while
// Forklift is still coming up, and Once would latch that failure forever, where
// this simply builds again on the next call.
func (c *inventoryClient) client(ctx context.Context) (*http.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.http != nil {
		return c.http, nil
	}
	if c.Reader == nil {
		return nil, fmt.Errorf("inventory client has no reader for the %s secret", inventoryCASecret)
	}

	secret := &corev1.Secret{}
	if err := c.Reader.Get(ctx, types.NamespacedName{
		Namespace: c.Namespace, Name: inventoryCASecret,
	}, secret); err != nil {
		return nil, fmt.Errorf("reading the Forklift inventory CA: %w", err)
	}
	ca, ok := secret.Data[inventoryCAKey]
	if !ok || len(ca) == 0 {
		return nil, fmt.Errorf("secret %s/%s carries no %s", c.Namespace, inventoryCASecret, inventoryCAKey)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("the %s in %s/%s is not a valid PEM bundle", inventoryCAKey, c.Namespace, inventoryCASecret)
	}

	c.http = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
	}
	return c.http, nil
}

func (c *inventoryClient) token() (string, error) {
	path := c.TokenFile
	if path == "" {
		path = inventoryTokenFile
	}
	b, err := os.ReadFile(path) // #nosec G304 -- fixed path, or a test override
	if err != nil {
		return "", fmt.Errorf("reading the ServiceAccount token for the inventory call: %w", err)
	}
	return string(b), nil
}
