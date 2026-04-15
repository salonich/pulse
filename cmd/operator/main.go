// Command operator is the Pulse Kubernetes operator.
// It watches LLMBackend CRDs and reconciles cluster state:
// sidecar injection labels, pricing ConfigMaps, ServiceMonitors, GrafanaDashboards.
package main

import (
	"flag"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	pulseaiv1alpha1 "github.com/velorai/pulse/api/v1alpha1"
	"github.com/velorai/pulse/internal/controller"
	pulsewebhook "github.com/velorai/pulse/internal/webhook"
	"github.com/velorai/pulse/internal/pricing"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(pulseaiv1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		probeAddr            string
		webhookPort          int
		enableLeaderElection bool
		proxyImage           string
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Address for the metrics endpoint.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Address for the health probe endpoint.")
	flag.IntVar(&webhookPort, "webhook-port", 9443, "Port for the mutating webhook server.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", true, "Enable leader election for operator HA.")
	flag.StringVar(&proxyImage, "proxy-image", "ghcr.io/velorai/pulse-proxy:latest", "Proxy sidecar image to inject.")
	opts := zap.Options{Development: os.Getenv("DEV_MODE") == "true"}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	logger := ctrl.Log.WithName("operator")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "pulse.velorai.com",
		WebhookServer: webhook.NewServer(webhook.Options{
			Port: webhookPort,
		}),
	})
	if err != nil {
		logger.Error(err, "unable to create manager")
		os.Exit(1)
	}

	pt := pricing.New()

	if err := (&controller.LLMBackendReconciler{
		Client:  mgr.GetClient(),
		Scheme:  mgr.GetScheme(),
		Pricing: pt,
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to setup LLMBackend controller")
		os.Exit(1)
	}

	// Register the mutating webhook for pod injection.
	mgr.GetWebhookServer().Register("/mutate-v1-pod", &webhook.Admission{
		Handler: &pulsewebhook.PodInjector{
			Client:     mgr.GetClient(),
			ProxyImage: proxyImage,
		},
	})

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "unable to setup health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error(err, "unable to setup ready check")
		os.Exit(1)
	}

	logger.Info("starting operator", "metrics-addr", metricsAddr, "leader-elect", enableLeaderElection)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "operator exited with error")
		os.Exit(1)
	}
}
