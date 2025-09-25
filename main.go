/*
Copyright 2021 The Kubernetes Authors.

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
	"context"
	"crypto/tls"
	"flag"
	"math/rand"
	"os"
	"time"

	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/textlogger"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/feature"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	infrav1 "sigs.k8s.io/cluster-api-provider-kubevirt/api/v1alpha1"
	"sigs.k8s.io/cluster-api-provider-kubevirt/controllers"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/infracluster"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/kubevirt"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/webhookhandler"
	"sigs.k8s.io/cluster-api-provider-kubevirt/pkg/workloadcluster"
	// +kubebuilder:scaffold:imports
)

var (
	setupLog = ctrl.Log.WithName("setup")

	// flags.
	metricsBindAddr      string
	enableLeaderElection bool
	syncPeriod           time.Duration
	concurrency          int
	healthAddr           string
	webhookPort          int
	webhookCertDir       string
	watchNamespace       string
)

func init() {
	klog.InitFlags(nil)
}

func registerScheme() (*runtime.Scheme, error) {
	myscheme := runtime.NewScheme()

	for _, f := range []func(*runtime.Scheme) error{
		scheme.AddToScheme,
		infrav1.AddToScheme,
		clusterv1.AddToScheme,
		kubevirtv1.AddToScheme,
		cdiv1.AddToScheme,
		// +kubebuilder:scaffold:scheme
	} {
		if err := f(myscheme); err != nil {
			return nil, err
		}
	}
	return myscheme, nil
}

func initFlags(fs *pflag.FlagSet) {
	fs.StringVar(&metricsBindAddr, "metrics-bind-addr", "localhost:8080",
		"The address the metric endpoint binds to.")
	fs.IntVar(&concurrency, "concurrency", 10,
		"The number of machines to process simultaneously")
	fs.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")
	fs.DurationVar(&syncPeriod, "sync-period", 60*time.Second,
		"The minimum interval at which watched resources are reconciled (e.g. 15m)")
	fs.StringVar(&healthAddr, "health-addr", ":9440",
		"The address the health endpoint binds to.")
	fs.IntVar(&webhookPort, "webhook-port", 9443,
		"Webhook Server port")
	fs.StringVar(&webhookCertDir, "webhook-cert-dir", "/tmp/k8s-webhook-server/serving-certs/",
		"Webhook cert dir, only used when webhook-port is specified.")
	fs.StringVar(&watchNamespace, "namespace", "",
		"Namespace that the controller watches to reconcile cluster-api objects. If unspecified, the controller watches for cluster-api objects across all namespaces.")

	feature.MutableGates.AddFlag(fs)
}

func main() {
	rand.New(rand.NewSource(time.Now().UnixNano()))

	initFlags(pflag.CommandLine)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.CommandLine.SetNormalizeFunc(cliflag.WordSepNormalizeFunc)
	pflag.Parse()

	ctrl.SetLogger(textlogger.NewLogger(textlogger.NewConfig()))

	myscheme, err := registerScheme()
	if err != nil {
		setupLog.Error(err, "can't register scheme")
		os.Exit(1)
	}

	var defaultNamespaces map[string]cache.Config
	if watchNamespace != "" {
		setupLog.Info("Watching cluster-api objects only in namespace for reconciliation", "namespace", watchNamespace)
		defaultNamespaces = map[string]cache.Config{
			watchNamespace: {},
		}
	}

	webhookOptions := webhook.Options{
		Port:    webhookPort,
		CertDir: webhookCertDir,
		TLSOpts: []func(*tls.Config){
			func(t *tls.Config) {
				t.MinVersion = tls.VersionTLS12
			},
		},
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:           myscheme,
		Metrics:          server.Options{BindAddress: metricsBindAddr},
		LeaderElection:   enableLeaderElection,
		LeaderElectionID: "controller-leader-election-capk",
		Cache: cache.Options{
			SyncPeriod:        &syncPeriod,
			DefaultNamespaces: defaultNamespaces,
		},
		HealthProbeBindAddress: healthAddr,
		WebhookServer:          webhook.NewServer(webhookOptions),
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Setup the context that's going to be used in controllers and for the manager.
	ctx := ctrl.SetupSignalHandler()

	setupChecks(mgr)
	setupReconcilers(ctx, mgr)
	setupWebhooks(mgr)

	// +kubebuilder:scaffold:builder
	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func setupChecks(mgr ctrl.Manager) {
	if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to create ready check")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to create health check")
		os.Exit(1)
	}
}

func setupReconcilers(ctx context.Context, mgr ctrl.Manager) {
	noCachedClient, err := k8sclient.New(mgr.GetConfig(), k8sclient.Options{Scheme: mgr.GetClient().Scheme()})
	if err != nil {
		setupLog.Error(err, "unable to create controller; failed to generate no-cached client")
		os.Exit(1)
	}

	if err := (&controllers.KubevirtMachineReconciler{
		Client:          mgr.GetClient(),
		InfraCluster:    infracluster.New(mgr.GetClient(), noCachedClient),
		WorkloadCluster: workloadcluster.New(mgr.GetClient()),
		MachineFactory:  kubevirt.DefaultMachineFactory{},
	}).SetupWithManager(ctx, mgr, controller.Options{
		MaxConcurrentReconciles: concurrency,
	}); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "reconciler")
		os.Exit(1)
	}

	if err := (&controllers.KubevirtClusterReconciler{
		Client:       mgr.GetClient(),
		APIReader:    mgr.GetAPIReader(),
		InfraCluster: infracluster.New(mgr.GetClient(), noCachedClient),
		Log:          ctrl.Log.WithName("controllers").WithName("KubevirtCluster"),
	}).SetupWithManager(ctx, mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "KubevirtCluster")
		os.Exit(1)
	}
}

func setupWebhooks(mgr ctrl.Manager) {
	if err := webhookhandler.SetupWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create webhook", "webhook", "KubevirtMachineTemplate")
		os.Exit(1)
	}
}
