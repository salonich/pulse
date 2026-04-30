package controller

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	pulseaiv1alpha1 "github.com/velorai/pulse/api/v1alpha1"
	"github.com/velorai/pulse/internal/observability"
)

// serviceMonitorResource is the OwnedResource adapter for kube-prometheus-stack ServiceMonitors.
type serviceMonitorResource struct{}

func (serviceMonitorResource) Name() string          { return "ServiceMonitor" }
func (serviceMonitorResource) ConditionType() string { return pulseaiv1alpha1.ConditionServiceMonitorReady }
func (serviceMonitorResource) Enabled(b *pulseaiv1alpha1.LLMBackend) bool {
	return b.Spec.Observability.Prometheus
}
func (serviceMonitorResource) Build(b *pulseaiv1alpha1.LLMBackend) *unstructured.Unstructured {
	return observability.ServiceMonitorForBackend(b.Namespace, b.Name)
}

// grafanaDashboardResource is the OwnedResource adapter for the Grafana operator's CRD.
type grafanaDashboardResource struct{}

func (grafanaDashboardResource) Name() string { return "GrafanaDashboard" }
func (grafanaDashboardResource) ConditionType() string {
	return pulseaiv1alpha1.ConditionGrafanaDashboardReady
}
func (grafanaDashboardResource) Enabled(b *pulseaiv1alpha1.LLMBackend) bool {
	return b.Spec.Observability.Grafana
}
func (grafanaDashboardResource) Build(b *pulseaiv1alpha1.LLMBackend) *unstructured.Unstructured {
	return observability.GrafanaDashboardForBackend(b.Namespace, b.Name)
}

// ownedResources is the canonical list reconciled per LLMBackend.
// New owned resources are added by appending here.
var ownedResources = []OwnedResource{
	serviceMonitorResource{},
	grafanaDashboardResource{},
}
