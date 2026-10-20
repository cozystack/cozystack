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

package main

import (
	"errors"
	"flag"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	gpuv1alpha1 "github.com/cozystack/cozystack/api/gpu/v1alpha1"
	"github.com/cozystack/cozystack/internal/gpu/agent"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(gpuv1alpha1.AddToScheme(scheme))
}

type options struct {
	nodeName  string
	sysfs     string
	socket    string
	interval  time.Duration
	probeAddr string
}

func parseFlags(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("gpu-node-agent", flag.ContinueOnError)
	fs.StringVar(&o.nodeName, "node-name", os.Getenv("NODE_NAME"), "Name of this node; the agent writes only the GPUNode of that name.")
	fs.StringVar(&o.sysfs, "sysfs", "/sys", "Where the host's sysfs is mounted.")
	fs.StringVar(&o.socket, "pod-resources-socket", agent.PodResourcesSocket, "kubelet's pod-resources socket.")
	fs.DurationVar(&o.interval, "interval", 30*time.Second, "Time between inventory syncs.")
	fs.StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.nodeName == "" {
		return o, errors.New("--node-name or NODE_NAME is required")
	}
	if o.interval < time.Second {
		return o, errors.New("--interval must be at least 1s")
	}
	return o, nil
}

func main() {
	ctrl.SetLogger(zap.New())
	setupLog := ctrl.Log.WithName("setup")

	o, err := parseFlags(os.Args[1:])
	if err != nil {
		setupLog.Error(err, "invalid flags")
		os.Exit(2)
	}

	cfg := ctrl.GetConfigOrDie()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: o.probeAddr,
	})
	if err != nil {
		setupLog.Error(err, "unable to create manager")
		os.Exit(1)
	}
	// The manager's client would cache and so watch every pod in the
	// cluster; the agent lists only its own node's.
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "unable to create client")
		os.Exit(1)
	}
	podResources, conn, err := agent.Dial(o.socket)
	if err != nil {
		setupLog.Error(err, "invalid pod-resources socket")
		os.Exit(1)
	}
	defer func() { _ = conn.Close() }()

	a := &agent.Agent{
		NodeName:     o.nodeName,
		Sysfs:        o.sysfs,
		PodResources: podResources,
		Client:       c,
		Interval:     o.interval,
	}
	if err := mgr.Add(a); err != nil {
		setupLog.Error(err, "unable to add agent")
		os.Exit(1)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("synced", a.Ready); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting GPU node agent", "node", o.nodeName)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "agent stopped")
		os.Exit(1)
	}
}
