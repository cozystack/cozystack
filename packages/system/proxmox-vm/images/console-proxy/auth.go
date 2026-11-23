package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// kube-apiserver reaches an aggregated API with its front-proxy client
// certificate and tells it who the caller is in request headers. The
// certificate authority, the names it may carry and the header names all
// live in kube-system/extension-apiserver-authentication, which the
// apiserver keeps current; the proxy rereads it on a timer.

const (
	authConfigMapNamespace = "kube-system"
	authConfigMapName      = "extension-apiserver-authentication"
)

var errNoClientCert = errors.New("no front-proxy client certificate")

// userInfo is the caller as the front proxy described it.
type userInfo struct {
	Name   string
	Groups []string
	Extra  map[string][]string
}

type frontProxyAuth struct {
	mu            sync.RWMutex
	roots         *x509.CertPool
	allowedNames  map[string]struct{}
	userHeaders   []string
	groupHeaders  []string
	extraPrefixes []string
	loaded        bool
}

func (a *frontProxyAuth) load(ctx context.Context, client kubernetes.Interface) error {
	cm, err := client.CoreV1().ConfigMaps(authConfigMapNamespace).Get(ctx, authConfigMapName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("reading %s/%s: %w", authConfigMapNamespace, authConfigMapName, err)
	}
	return a.configure(cm.Data)
}

func (a *frontProxyAuth) configure(data map[string]string) error {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(data["requestheader-client-ca-file"])) {
		return errors.New("requestheader-client-ca-file holds no certificate")
	}
	list := func(key string, fallback []string) ([]string, error) {
		raw, ok := data[key]
		if !ok || raw == "" {
			return fallback, nil
		}
		var out []string
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		return out, nil
	}
	names, err := list("requestheader-allowed-names", nil)
	if err != nil {
		return err
	}
	users, err := list("requestheader-username-headers", []string{"X-Remote-User"})
	if err != nil {
		return err
	}
	groups, err := list("requestheader-group-headers", []string{"X-Remote-Group"})
	if err != nil {
		return err
	}
	prefixes, err := list("requestheader-extra-headers-prefix", []string{"X-Remote-Extra-"})
	if err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.roots = roots
	a.allowedNames = make(map[string]struct{}, len(names))
	for _, n := range names {
		a.allowedNames[n] = struct{}{}
	}
	a.userHeaders = users
	a.groupHeaders = groups
	a.extraPrefixes = prefixes
	a.loaded = true
	return nil
}

// run rereads the configuration every interval until ctx ends.
func (a *frontProxyAuth) run(ctx context.Context, client kubernetes.Interface, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.load(ctx, client); err != nil {
				slog.Warn("front-proxy configuration not refreshed", "error", err)
			}
		}
	}
}

// verify checks that the request came through the front proxy and returns
// the caller it named, if any.
func (a *frontProxyAuth) verify(r *http.Request) (*userInfo, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.loaded {
		return nil, errors.New("front-proxy configuration not loaded")
	}
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return nil, errNoClientCert
	}
	leaf := r.TLS.PeerCertificates[0]
	intermediates := x509.NewCertPool()
	for _, c := range r.TLS.PeerCertificates[1:] {
		intermediates.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         a.roots,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return nil, fmt.Errorf("front-proxy certificate: %w", err)
	}
	if len(a.allowedNames) > 0 {
		if _, ok := a.allowedNames[leaf.Subject.CommonName]; !ok {
			return nil, fmt.Errorf("front-proxy certificate name %q not allowed", leaf.Subject.CommonName)
		}
	}

	u := &userInfo{Extra: map[string][]string{}}
	for _, h := range a.userHeaders {
		if v := r.Header.Get(h); v != "" {
			u.Name = v
			break
		}
	}
	for _, h := range a.groupHeaders {
		u.Groups = append(u.Groups, r.Header.Values(h)...)
	}
	for key, values := range r.Header {
		for _, prefix := range a.extraPrefixes {
			if !strings.HasPrefix(strings.ToLower(key), strings.ToLower(prefix)) {
				continue
			}
			name := strings.ToLower(key[len(prefix):])
			if unescaped, err := url.PathUnescape(name); err == nil {
				name = unescaped
			}
			u.Extra[name] = append(u.Extra[name], values...)
		}
	}
	return u, nil
}
