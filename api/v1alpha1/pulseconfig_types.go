package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PulseConfigSpec is the cluster-wide configuration for Pulse.
// Apply one instance named `cluster-config` in `pulse-system`.
type PulseConfigSpec struct {
	// ProxyImage is the sidecar container image injected into target pods.
	// +kubebuilder:default="ghcr.io/velorai/pulse-proxy:latest"
	ProxyImage string `json:"proxyImage,omitempty"`

	// CollectorEndpoint is the trace collector service URL.
	// +kubebuilder:default="http://pulse-collector.pulse-system:9090"
	CollectorEndpoint string `json:"collectorEndpoint,omitempty"`

	// RetentionDays controls how long traces are kept. Default: 90.
	// +kubebuilder:default=90
	RetentionDays int32 `json:"retentionDays,omitempty"`
}

// PulseConfigStatus reflects the observed state of the cluster config.
type PulseConfigStatus struct {
	// +optional
	Conditions         []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	// +optional
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=pulsecfg

// PulseConfig is the cluster-wide configuration for the Pulse operator.
type PulseConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec   PulseConfigSpec   `json:"spec,omitempty"`
	Status PulseConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PulseConfigList contains a list of PulseConfig.
type PulseConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PulseConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PulseConfig{}, &PulseConfigList{})
}
