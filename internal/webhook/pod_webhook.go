// Package webhook implements the mutating admission webhook for proxy sidecar injection.
package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	pulseaiv1alpha1 "github.com/velorai/pulse/api/v1alpha1"
)

const (
	proxyContainerName = "pulse-proxy"
	injectLabel        = "pulse.velorai.com/inject"
	injectLabelValue   = "enabled"
	pricingCMName      = "pulse-pricing"
	pricingCMNS        = "pulse-system"
	defaultProxyImage  = "ghcr.io/velorai/pulse-proxy:latest"
	defaultCollectorURL = "http://pulse-collector.pulse-system:9090"
)

// PodInjector is the admission.Handler that injects the pulse-proxy sidecar.
type PodInjector struct {
	Client     client.Client
	Decoder    admission.Decoder
	ProxyImage string
}

// InjectDecoder satisfies admission.DecoderInjector so controller-runtime injects the decoder.
func (h *PodInjector) InjectDecoder(d admission.Decoder) error {
	h.Decoder = d
	return nil
}

// Handle processes a pod admission request and injects the proxy sidecar when appropriate.
func (h *PodInjector) Handle(ctx context.Context, req admission.Request) admission.Response {
	logger := log.FromContext(ctx)

	pod := &corev1.Pod{}
	if err := h.Decoder.DecodeRaw(req.Object, pod); err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("decoding pod: %w", err))
	}

	// Step 1: Is the namespace labelled for injection?
	var ns corev1.Namespace
	if err := h.Client.Get(ctx, types.NamespacedName{Name: req.Namespace}, &ns); err != nil {
		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("fetching namespace: %w", err))
	}
	if ns.Labels[injectLabel] != injectLabelValue {
		return admission.Allowed("namespace not labelled for injection")
	}

	// Step 2: Does any LLMBackend in this namespace target a service matching this pod?
	backend, err := h.findMatchingBackend(ctx, req.Namespace, pod)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("finding LLMBackend: %w", err))
	}
	if backend == nil {
		return admission.Allowed("no matching LLMBackend found for pod")
	}

	// Step 3: Is the pod already injected?
	for _, c := range pod.Spec.Containers {
		if c.Name == proxyContainerName {
			return admission.Allowed("already injected")
		}
	}
	for _, c := range pod.Spec.InitContainers {
		if c.Name == proxyContainerName {
			return admission.Allowed("already injected (init)")
		}
	}

	// Step 4: Build and return the injection patch.
	logger.Info("injecting pulse-proxy sidecar",
		"pod", pod.Name, "namespace", req.Namespace, "backend", backend.Name)

	mutated := pod.DeepCopy()
	h.inject(mutated, backend)

	marshalledPod, err := json.Marshal(mutated)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, marshalledPod)
}

// findMatchingBackend returns the first LLMBackend in the namespace whose targetService
// selector matches the pod's labels, or nil if none match.
func (h *PodInjector) findMatchingBackend(ctx context.Context, namespace string, pod *corev1.Pod) (*pulseaiv1alpha1.LLMBackend, error) {
	var list pulseaiv1alpha1.LLMBackendList
	if err := h.Client.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing LLMBackends: %w", err)
	}

	for i := range list.Items {
		backend := &list.Items[i]
		svc := &corev1.Service{}
		err := h.Client.Get(ctx, types.NamespacedName{
			Name:      backend.Spec.TargetService.Name,
			Namespace: backend.Spec.TargetService.Namespace,
		}, svc)
		if err != nil {
			continue // service not found yet; skip
		}
		if selectorMatches(svc.Spec.Selector, pod.Labels) {
			return backend, nil
		}
	}
	return nil, nil
}

// selectorMatches returns true if every key=value in selector is present in labels.
func selectorMatches(selector, labels map[string]string) bool {
	if len(selector) == 0 {
		return false // empty selector matches nothing for safety
	}
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// inject mutates the pod spec to add the proxy sidecar container, rewrite LLM env vars,
// and mount the pricing ConfigMap.
func (h *PodInjector) inject(pod *corev1.Pod, backend *pulseaiv1alpha1.LLMBackend) {
	image := h.ProxyImage
	if image == "" {
		image = defaultProxyImage
	}

	// Rewrite existing LLM env vars in all app containers.
	for i := range pod.Spec.Containers {
		rewriteLLMEnvVars(&pod.Spec.Containers[i])
	}

	// Add pricing ConfigMap volume.
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: "pulse-pricing",
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: pricingCMName},
			},
		},
	})

	// Build proxy sidecar container.
	proxy := corev1.Container{
		Name:  proxyContainerName,
		Image: image,
		Env: []corev1.EnvVar{
			{Name: "PULSE_COLLECTOR_URL", Value: defaultCollectorURL},
			{Name: "PULSE_NAMESPACE", Value: backend.Namespace},
			{Name: "PULSE_LLMBACKEND_NAME", Value: backend.Name},
			{Name: "PULSE_PRICING_PATH", Value: "/etc/pulse/pricing.json"},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "pulse-pricing", MountPath: "/etc/pulse", ReadOnly: true},
		},
		Resources: corev1.ResourceRequirements{},
		SecurityContext: &corev1.SecurityContext{
			RunAsUser:                &[]int64{65534}[0],
			ReadOnlyRootFilesystem:   &[]bool{true}[0],
			AllowPrivilegeEscalation: &[]bool{false}[0],
		},
	}
	pod.Spec.Containers = append(pod.Spec.Containers, proxy)
}

// rewriteLLMEnvVars overwrites ANTHROPIC_BASE_URL / OPENAI_BASE_URL in an existing container
// so LLM calls are routed through the proxy at localhost:8888.
func rewriteLLMEnvVars(c *corev1.Container) {
	rewrites := map[string]string{
		"ANTHROPIC_BASE_URL": "http://localhost:8888/anthropic",
		"OPENAI_BASE_URL":    "http://localhost:8888/openai",
	}
	for i, env := range c.Env {
		if newVal, ok := rewrites[env.Name]; ok {
			c.Env[i].Value = newVal
		}
	}
}
