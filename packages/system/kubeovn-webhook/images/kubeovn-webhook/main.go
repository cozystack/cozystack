package main

import (
	"flag"
	"log"
	"net/http"
	"time"
)

var (
	PortSecurityGlobal bool
	RoutesGlobal       string
)

func main() {
	var (
		tlsCertFile string
		tlsKeyFile  string
	)

	flag.StringVar(&tlsCertFile, "tls-cert-file", "/etc/webhook/certs/tls.crt", "TLS certificate file.")
	flag.StringVar(&tlsKeyFile, "tls-key-file", "/etc/webhook/certs/tls.key", "TLS key file.")
	flag.BoolVar(&PortSecurityGlobal, "port-security", true, "If false, skip adding port_security unless specified by the Namespace.")
	flag.StringVar(&RoutesGlobal, "routes", "", "Default ovn.kubernetes.io/routes if not in Namespace.")

	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/mutate-pods", HandleMutatePods)

	tlsConfig, err := newReloadingTLSConfig(tlsCertFile, tlsKeyFile)
	if err != nil {
		log.Fatalf("Failed to load key pair: %v", err)
	}

	server := &http.Server{
		Addr:              ":8443",
		TLSConfig:         tlsConfig,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("Starting webhook server on %s", server.Addr)
	if err := server.ListenAndServeTLS("", ""); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
