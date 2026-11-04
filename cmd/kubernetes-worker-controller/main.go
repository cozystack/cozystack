package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"time"

	workers "github.com/cozystack/cozystack/api/workers/v1alpha1"
	workeradmission "github.com/cozystack/cozystack/internal/kubernetesworker/admission"
	"github.com/cozystack/cozystack/internal/kubernetesworker/controller"
	"github.com/cozystack/cozystack/internal/kubernetesworker/lifecycle"
	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	ar "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	ca "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

type options struct {
	kubeconfig       string
	defaultClass     string
	certDir          string
	bind             string
	port             int
	probes           string
	leader           bool
	namespace        string
	removeMutating   string
	removeValidating string
}

func defaultOptions() options {
	return options{defaultClass: "talos", certDir: "/etc/worker-webhook/certs", bind: "0.0.0.0", port: 9443, probes: ":8081", namespace: "cozy-system"}
}
func (o options) validate() error {
	if (o.removeMutating == "") != (o.removeValidating == "") {
		return fmt.Errorf("both admission configuration names are required for cleanup")
	}
	if len(validation.IsDNS1123Subdomain(o.defaultClass)) > 0 {
		return fmt.Errorf("default-worker-class must name a WorkerClass")
	}
	if o.certDir == "" {
		return fmt.Errorf("cert-dir must not be empty")
	}
	if o.port < 1 || o.port > 65535 {
		return fmt.Errorf("webhook-port must be between 1 and 65535")
	}
	return nil
}
func main() {
	if err := run(ctrl.SetupSignalHandler(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) error {
	o := defaultOptions()
	flags := flag.NewFlagSet("kubernetes-worker-controller", flag.ContinueOnError)
	flags.StringVar(&o.kubeconfig, "kubeconfig", "", "Kubeconfig; empty uses the standard in-cluster client configuration.")
	flags.StringVar(&o.defaultClass, "default-worker-class", o.defaultClass, "WorkerClass assigned to new pools without a class.")
	flags.StringVar(&o.certDir, "cert-dir", o.certDir, "Directory containing tls.crt and tls.key.")
	flags.StringVar(&o.bind, "webhook-bind-address", o.bind, "Webhook listener address.")
	flags.IntVar(&o.port, "webhook-port", o.port, "Webhook HTTPS port.")
	flags.StringVar(&o.probes, "health-probe-bind-address", o.probes, "Health and readiness listener address.")
	flags.BoolVar(&o.leader, "leader-elect", false, "Enable controller leader election.")
	flags.StringVar(&o.namespace, "leader-election-namespace", o.namespace, "Namespace for the leader election lease.")
	flags.StringVar(&o.removeMutating, "unregister-mutating-webhook", "", "Remove this registration and exit before backend teardown.")
	flags.StringVar(&o.removeValidating, "unregister-validating-webhook", "", "Remove this registration and exit before backend teardown.")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := o.validate(); err != nil {
		return err
	}
	ctrl.SetLogger(zap.New())
	var cfg *rest.Config
	var err error
	if o.kubeconfig != "" {
		cfg, err = clientcmd.BuildConfigFromFlags("", o.kubeconfig)
	} else {
		cfg, err = ctrl.GetConfig()
	}
	if err != nil {
		return err
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{workers.AddToScheme, helmv2.AddToScheme, ar.AddToScheme} {
		if err := add(scheme); err != nil {
			return err
		}
	}
	if o.removeMutating != "" {
		c, err := client.New(cfg, client.Options{Scheme: scheme})
		if err != nil {
			return err
		}
		cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return lifecycle.Unregister(cleanupCtx, c, o.removeMutating, o.removeValidating)
	}
	shutdown := 10 * time.Second
	server := webhook.NewServer(webhook.Options{Host: o.bind, Port: o.port, CertDir: o.certDir, TLSOpts: []func(*tls.Config){func(c *tls.Config) { c.MinVersion = tls.VersionTLS12; c.NextProtos = []string{"http/1.1"} }}})
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, WebhookServer: server, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: o.probes, LeaderElection: o.leader, LeaderElectionNamespace: o.namespace, LeaderElectionID: "kubernetes-worker-classes.cozystack.io", GracefulShutdownTimeout: &shutdown})
	if err != nil {
		return err
	}
	if err = (&controller.Reconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader()}).SetupWithManager(mgr); err != nil {
		return err
	}
	mgr.GetWebhookServer().Register("/mutate-worker-pool", &ca.Webhook{Handler: &workeradmission.Mutator{DefaultWorkerClass: o.defaultClass}})
	mgr.GetWebhookServer().Register("/validate-worker-pool", &ca.Webhook{Handler: &workeradmission.Validator{Reader: mgr.GetAPIReader()}})
	if err = mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err = mgr.AddReadyzCheck("webhook", mgr.GetWebhookServer().StartedChecker()); err != nil {
		return err
	}
	return mgr.Start(ctx)
}
