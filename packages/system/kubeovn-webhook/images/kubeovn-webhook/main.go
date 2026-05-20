package main

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"log"
	"net/http"
	"os"
)

var (
	PortSecurityGlobal bool
	RoutesGlobal       string
)

func main() {
	var (
		tlsCertFile  string
		tlsKeyFile   string
		clientCAFile string
	)

	flag.StringVar(&tlsCertFile, "tls-cert-file", "/etc/webhook/certs/tls.crt", "TLS certificate file.")
	flag.StringVar(&tlsKeyFile, "tls-key-file", "/etc/webhook/certs/tls.key", "TLS key file.")
	flag.StringVar(&clientCAFile, "client-ca-file", "", "CA certificate for verifying client certificates (mTLS). If empty, client certificate verification is disabled.")
	flag.BoolVar(&PortSecurityGlobal, "port-security", true, "If false, skip adding port_security unless specified by the Namespace.")
	flag.StringVar(&RoutesGlobal, "routes", "", "Default ovn.kubernetes.io/routes if not in Namespace.")

	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/mutate-pods", HandleMutatePods)

	tlsCert, err := tls.LoadX509KeyPair(tlsCertFile, tlsKeyFile)
	if err != nil {
		log.Fatalf("Failed to load key pair: %v", err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
	}

	if clientCAFile != "" {
		clientCAPool := x509.NewCertPool()
		clientCAData, err := os.ReadFile(clientCAFile)
		if err != nil {
			log.Fatalf("Failed to read client CA file: %v", err)
		}
		if !clientCAPool.AppendCertsFromPEM(clientCAData) {
			log.Fatalf("Failed to parse client CA certificate")
		}
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		tlsConfig.ClientCAs = clientCAPool
		log.Printf("mTLS enabled: requiring client certificates signed by %s", clientCAFile)
	}

	server := &http.Server{
		Addr:      ":8443",
		TLSConfig: tlsConfig,
		Handler:   mux,
	}

	log.Printf("Starting webhook server on %s", server.Addr)
	if err := server.ListenAndServeTLS("", ""); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
