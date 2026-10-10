/*
Copyright 2025 The Cozystack Authors.

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

package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	pxv1 "github.com/cozystack/cozystack/api/proxmox/v1alpha1"
	pnc "github.com/cozystack/cozystack/internal/proxmoxnetworkcontroller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(pxv1.AddToScheme(scheme))
	utilruntime.Must(pnc.AddMirrorsToScheme(scheme))
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var subnetPruneTimeout time.Duration
	var ovnNBAddress, ovnTLSSecret string
	var manageGatewayChassis bool
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election. The VLAN allocator is only race-free with a single active instance.")
	flag.BoolVar(&secureMetrics, "metrics-secure", false,
		"Serve the metrics endpoint over HTTPS with authentication and authorization.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false, "If set, HTTP/2 will be enabled for the metrics server")
	flag.DurationVar(&subnetPruneTimeout, "subnet-prune-timeout", pnc.DefaultSubnetPruneTimeout,
		"How long each deleted subnet may stay listed on its Vlan's status before the VLAN is released anyway "+
			"(Kube-OVN drops the name after deleting the logical switch). Applies to deleting networks and to "+
			"the zone's collection of orphaned Vlans.")
	flag.BoolVar(&manageGatewayChassis, "manage-gateway-chassis", true,
		"Give the VPC router port of every Proxmox network an OVN HA chassis group of the zone's trunk nodes, "+
			"written to the OVN northbound database. Without it Kube-OVN's OVN drops the VMs' ARP requests for "+
			"their gateway. When false, GatewayReady is Unknown and Ready does not wait for it.")
	flag.StringVar(&ovnNBAddress, "ovn-nb-address", "ssl:ovn-nb.cozy-kubeovn.svc:6641",
		"OVN northbound database, as a comma-separated list of ssl:<host>:<port> or tcp:<host>:<port>, tried in order. "+
			"Writes must reach the RAFT leader; Kube-OVN's ovn-nb Service selects it.")
	flag.StringVar(&ovnTLSSecret, "ovn-tls-secret", "cozy-kubeovn/kube-ovn-tls",
		"<namespace>/<name> of the secret with the OVN client TLS material (keys cacert, cert, key), read through the API.")
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Disable HTTP/2 by default to avoid the Stream Cancellation and Rapid Reset
	// CVEs (GHSA-qppj-fm5r-hxr3, GHSA-4374-p667-p6c8).
	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, func(c *tls.Config) { c.NextProtos = []string{"http/1.1"} })
	}
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}
	if secureMetrics {
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	config := ctrl.GetConfigOrDie()
	config.QPS = 20.0
	config.Burst = 40

	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "proxmox-network-controller.proxmox.cozystack.io",
		// The only pods this controller looks at are VPC egress gateway pods.
		Cache: cache.Options{ByObject: map[client.Object]cache.ByObject{
			&corev1.Pod{}: {Label: pnc.VEGPodSelector()},
		}},
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	var gateways *pnc.GatewayManager
	if manageGatewayChassis {
		gateways, err = newGatewayManager(mgr.GetAPIReader(), ovnNBAddress, ovnTLSSecret)
		if err != nil {
			setupLog.Error(err, "unable to set up the OVN NB client")
			os.Exit(1)
		}
		if err := mgr.Add(&pnc.GatewayCollector{Gateways: gateways, Reader: mgr.GetAPIReader()}); err != nil {
			setupLog.Error(err, "unable to set up the gateway collector")
			os.Exit(1)
		}
	} else {
		setupLog.Info("not managing gateway chassis groups; VMs on Proxmox networks get no ARP answer from their VPC gateway")
	}

	if err := (&pnc.NetworkReconciler{
		Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), SubnetPruneTimeout: subnetPruneTimeout,
		Gateways: gateways,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to set up controller", "controller", "ProxmoxNetwork")
		os.Exit(1)
	}
	if err := (&pnc.ZoneReconciler{
		Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), SubnetPruneTimeout: subnetPruneTimeout,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to set up controller", "controller", "ProxmoxNetworkZone")
		os.Exit(1)
	}

	if err := (&pnc.EgressGatewayPodReconciler{Client: mgr.GetClient(), Reader: mgr.GetAPIReader()}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to set up controller", "controller", "EgressGatewayPod")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// newGatewayManager connects the gateway manager to the OVN NB, with the TLS
// material of the secret for ssl: addresses.
func newGatewayManager(reader client.Reader, address, secret string) (*pnc.GatewayManager, error) {
	ns, name, ok := strings.Cut(secret, "/")
	if !ok || ns == "" || name == "" || strings.Contains(name, "/") {
		return nil, fmt.Errorf("--ovn-tls-secret %q: want <namespace>/<name>", secret)
	}
	nb, err := pnc.NewNBClient(address, &pnc.SecretTLSSource{
		Reader: reader, Secret: client.ObjectKey{Namespace: ns, Name: name},
	})
	if err != nil {
		return nil, err
	}
	return pnc.NewGatewayManager(nb), nil
}
