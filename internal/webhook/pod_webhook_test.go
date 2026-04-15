package webhook

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	pulseaiv1alpha1 "github.com/velorai/pulse/api/v1alpha1"
)

func TestSelectorMatches(t *testing.T) {
	tests := []struct {
		name     string
		selector map[string]string
		labels   map[string]string
		want     bool
	}{
		{
			name:     "exact match",
			selector: map[string]string{"app": "checkout"},
			labels:   map[string]string{"app": "checkout", "version": "v1"},
			want:     true,
		},
		{
			name:     "no match",
			selector: map[string]string{"app": "checkout"},
			labels:   map[string]string{"app": "other"},
			want:     false,
		},
		{
			name:     "empty selector returns false for safety",
			selector: map[string]string{},
			labels:   map[string]string{"app": "anything"},
			want:     false,
		},
		{
			name:     "nil selector returns false",
			selector: nil,
			labels:   map[string]string{"app": "anything"},
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectorMatches(tt.selector, tt.labels); got != tt.want {
				t.Errorf("selectorMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRewriteLLMEnvVars(t *testing.T) {
	c := &corev1.Container{
		Env: []corev1.EnvVar{
			{Name: "ANTHROPIC_BASE_URL", Value: "https://api.anthropic.com"},
			{Name: "OPENAI_BASE_URL", Value: "https://api.openai.com"},
			{Name: "OTHER_VAR", Value: "keep"},
		},
	}
	rewriteLLMEnvVars(c)

	for _, env := range c.Env {
		switch env.Name {
		case "ANTHROPIC_BASE_URL":
			if env.Value != "http://localhost:8888/anthropic" {
				t.Errorf("ANTHROPIC_BASE_URL = %q, want http://localhost:8888/anthropic", env.Value)
			}
		case "OPENAI_BASE_URL":
			if env.Value != "http://localhost:8888/openai" {
				t.Errorf("OPENAI_BASE_URL = %q, want http://localhost:8888/openai", env.Value)
			}
		case "OTHER_VAR":
			if env.Value != "keep" {
				t.Errorf("OTHER_VAR should not be rewritten, got %q", env.Value)
			}
		}
	}
}

func TestInject_AddsProxyContainer(t *testing.T) {
	h := &PodInjector{ProxyImage: "pulse-proxy:test"}
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "myapp:latest"},
			},
		},
	}

	backend := &pulseaiv1alpha1.LLMBackend{}
	backend.Name = "test-backend"
	backend.Namespace = "test-ns"

	h.inject(pod, backend)

	var found bool
	for _, c := range pod.Spec.Containers {
		if c.Name == proxyContainerName {
			found = true
			if c.Image != "pulse-proxy:test" {
				t.Errorf("proxy image = %q, want pulse-proxy:test", c.Image)
			}
			if *c.SecurityContext.RunAsUser != 65534 {
				t.Errorf("proxy RunAsUser = %d, want 65534", *c.SecurityContext.RunAsUser)
			}
		}
	}
	if !found {
		t.Errorf("pulse-proxy container not injected")
	}

	var foundVol bool
	for _, v := range pod.Spec.Volumes {
		if v.Name == "pulse-pricing" {
			foundVol = true
		}
	}
	if !foundVol {
		t.Error("pulse-pricing volume not added")
	}
}
