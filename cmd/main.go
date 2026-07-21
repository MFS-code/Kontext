package main

import (
	"context"
	"crypto/tls"
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	kontextv1alpha1 "github.com/MFS-code/Kontext/api/v1alpha1"
	"github.com/MFS-code/Kontext/internal/admission"
	"github.com/MFS-code/Kontext/internal/controller"
	"github.com/MFS-code/Kontext/internal/webhooktls"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kontextv1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	var enableLeaderElection bool
	var secureMetrics bool
	var reporterImage string

	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", false, "If set, the metrics endpoint is served securely.")
	flag.StringVar(
		&reporterImage,
		"reporter-image",
		envOrDefault("KONTEXT_REPORTER_IMAGE", ""),
		"Trusted reporter image used for optional stdout result capture.",
	)
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	config := ctrl.GetConfigOrDie()
	directClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "unable to create webhook bootstrap client")
		os.Exit(1)
	}
	certificateStore := &webhooktls.Store{}
	certificateLifecycle := webhooktls.NewLifecycle(
		directClient,
		certificateStore,
		webhooktls.DefaultOptions(),
	)
	if err := certificateLifecycle.Ensure(context.Background()); err != nil {
		setupLog.Error(err, "unable to bootstrap webhook certificates")
		os.Exit(1)
	}

	webhookServer := webhook.NewServer(webhook.Options{
		TLSOpts: []func(*tls.Config){disableHTTP2, webhooktls.TLSOption(certificateStore)},
	})
	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:   metricsAddr,
			SecureServing: secureMetrics,
			TLSOpts:       []func(*tls.Config){disableHTTP2},
		},
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "kontext.dev",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}
	webhookServer = mgr.GetWebhookServer()

	if err := (&controller.AgentRunReconciler{
		Client:        mgr.GetClient(),
		APIReader:     mgr.GetAPIReader(),
		Scheme:        mgr.GetScheme(),
		ReporterImage: reporterImage,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AgentRun")
		os.Exit(1)
	}

	if err := (&controller.AgentReconciler{
		Client:    mgr.GetClient(),
		APIReader: mgr.GetAPIReader(),
		Scheme:    mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Agent")
		os.Exit(1)
	}

	webhookServer.Register(
		admission.DefaultWebhookPath,
		admission.Handler(mgr.GetAPIReader(), mgr.GetScheme()),
	)
	if err := mgr.Add(certificateLifecycle); err != nil {
		setupLog.Error(err, "unable to add webhook certificate lifecycle")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("webhook-server", webhookServer.StartedChecker()); err != nil {
		setupLog.Error(err, "unable to set up webhook server ready check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("webhook-trust", certificateLifecycle.ReadinessCheck); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// disableHTTP2 forces HTTP/1.1 on inbound metrics and webhook TLS listeners.
// This mitigates HTTP/2 Rapid Reset (CVE-2023-44487) and stream-multiplexing
// DoS (CVE-2023-39325) on those server surfaces. It does not change the
// controller's outbound Kubernetes API client.
func disableHTTP2(c *tls.Config) {
	c.NextProtos = []string{"http/1.1"}
}

func envOrDefault(name string, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
