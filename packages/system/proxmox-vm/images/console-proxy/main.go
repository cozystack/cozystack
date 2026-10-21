// Command console-proxy serves subresources.proxmox.cozystack.io, the
// aggregated API behind the dashboard's VNC console, serial console and
// power buttons for virtual machines on a Proxmox VE substrate. It is the
// one place that holds the hypervisor's token on a tenant's behalf.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	var (
		listen      = flag.String("listen", ":8443", "address to serve TLS on")
		certFile    = flag.String("tls-cert-file", "/etc/console-proxy/tls/tls.crt", "serving certificate, reread when it changes")
		keyFile     = flag.String("tls-private-key-file", "/etc/console-proxy/tls/tls.key", "serving key")
		kubeconfig  = flag.String("kubeconfig", "", "kubeconfig to use instead of the in-cluster configuration")
		authRefresh = flag.Duration("authentication-refresh", 5*time.Minute, "how often the front-proxy configuration is reread")
	)
	flag.Parse()

	if err := run(*listen, *certFile, *keyFile, *kubeconfig, *authRefresh); err != nil {
		slog.Error("exiting", "error", err)
		os.Exit(1)
	}
}

func run(listen, certFile, keyFile, kubeconfig string, authRefresh time.Duration) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	config, err := restConfig(kubeconfig)
	if err != nil {
		return err
	}
	core, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}

	auth := &frontProxyAuth{}
	if err := auth.load(ctx, core); err != nil {
		return err
	}
	go auth.run(ctx, core, authRefresh)

	certs := &certReloader{certFile: certFile, keyFile: keyFile}
	if _, err := certs.get(); err != nil {
		return err
	}

	srv := &http.Server{
		Addr:    listen,
		Handler: newServer(auth, core, dyn).handler(),
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			// The apiserver presents its front-proxy certificate; it is
			// checked per request against the CA the apiserver publishes.
			ClientAuth:     tls.RequestClientCert,
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return certs.get() },
		},
		ReadHeaderTimeout: 30 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		slog.Info("serving", "group", apiGroup+"/"+apiVersion, "listen", listen)
		errc <- srv.ListenAndServeTLS("", "")
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := srv.Shutdown(shutdownCtx)
		if errors.Is(err, context.DeadlineExceeded) {
			return srv.Close()
		}
		return err
	}
}

func restConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("in-cluster configuration: %w (or pass --kubeconfig)", err)
	}
	return config, nil
}

// certReloader serves the certificate on disk and picks up a renewal by
// cert-manager without a restart.
type certReloader struct {
	certFile, keyFile string

	mu      sync.Mutex
	cert    *tls.Certificate
	modTime time.Time
	checked time.Time
}

func (c *certReloader) get() (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cert != nil && time.Since(c.checked) < 10*time.Second {
		return c.cert, nil
	}
	c.checked = time.Now()
	info, err := os.Stat(c.certFile)
	if err != nil {
		if c.cert != nil {
			return c.cert, nil
		}
		return nil, err
	}
	if c.cert != nil && info.ModTime().Equal(c.modTime) {
		return c.cert, nil
	}
	cert, err := tls.LoadX509KeyPair(c.certFile, c.keyFile)
	if err != nil {
		if c.cert != nil {
			slog.Warn("serving certificate not reloaded", "error", err)
			return c.cert, nil
		}
		return nil, err
	}
	c.cert = &cert
	c.modTime = info.ModTime()
	return c.cert, nil
}
