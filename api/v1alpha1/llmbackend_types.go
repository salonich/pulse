package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TargetService identifies the Kubernetes Service whose pods receive sidecar injection.
type TargetService struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`
}

// BackendRef references the upstream backend config for an LLM provider.
// Field naming aligns with wg-ai-gateway Backend GEP.
type BackendRef struct {
	// +kubebuilder:validation:Required
	Name      string `json:"name"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// CredentialRef references a K8s Secret containing the provider API key at key `api-key`.
type CredentialRef struct {
	// +kubebuilder:validation:Required
	Name      string `json:"name"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// Provider describes one LLM provider the target service calls.
type Provider struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=anthropic;openai;google;mistral;cohere;custom
	Name       string      `json:"name"`
	// +kubebuilder:validation:Required
	BackendRef BackendRef  `json:"backendRef"`
	// +optional
	CredentialRef *CredentialRef `json:"credentialRef,omitempty"`
}

// CaptureSpec controls what the proxy sidecar captures.
type CaptureSpec struct {
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`
}

// ObservabilitySpec controls automatic observability resource creation.
type ObservabilitySpec struct {
	// +kubebuilder:default=true
	Prometheus bool `json:"prometheus,omitempty"`
	// +kubebuilder:default=true
	Grafana bool `json:"grafana,omitempty"`
}

// LLMBackendSpec is the desired state of an LLMBackend.
type LLMBackendSpec struct {
	// +kubebuilder:validation:Required
	TargetService TargetService `json:"targetService"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Providers     []Provider    `json:"providers"`
	// +optional
	Capture       CaptureSpec   `json:"capture,omitempty"`
	// +optional
	Observability ObservabilitySpec `json:"observability,omitempty"`
}

// LLMBackendStatus is the observed state of an LLMBackend.
type LLMBackendStatus struct {
	// Conditions describe the current state of each owned resource.
	// One entry per resource (ServiceMonitor, GrafanaDashboard, ...) plus
	// the rollup `Ready` condition.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// ObservedGeneration is the spec generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// SidecarInjectedPods is the count of pods backing TargetService that
	// currently have the pulse-proxy container injected. This is a sampled
	// fact, not an aspiration — Ready=True requires this to be > 0 (or
	// captureMethod=ebpf, where the eBPF DaemonSet is the truth signal).
	// +optional
	SidecarInjectedPods int32 `json:"sidecarInjectedPods,omitempty"`
}

// Condition type constants. Each owned resource has a dedicated condition
// type set by the OwnedResource reconciler driver; Ready is the rollup.
const (
	ConditionPricingConfigMapReady = "PricingConfigMapReady"
	ConditionServiceMonitorReady   = "ServiceMonitorReady"
	ConditionGrafanaDashboardReady = "GrafanaDashboardReady"
	ConditionSidecarsInjected      = "SidecarsInjected"
	ConditionReady                 = "Ready"

	// Retained for backwards compatibility with status objects written by
	// older reconciler versions; not set by current code.
	ConditionSidecarInjected      = "SidecarInjected"
	ConditionPrometheusConfigured = "PrometheusConfigured"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=llmb
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// LLMBackend configures LLM observability for a target Kubernetes Service.
// The operator injects a proxy sidecar, creates a ServiceMonitor, and provisions a Grafana dashboard.
type LLMBackend struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec   LLMBackendSpec   `json:"spec,omitempty"`
	Status LLMBackendStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// LLMBackendList contains a list of LLMBackend.
type LLMBackendList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LLMBackend `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LLMBackend{}, &LLMBackendList{})
}
