// Package observability builds ServiceMonitor and GrafanaDashboard resources
// using unstructured objects to avoid importing operator SDK dependencies.
package observability

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var serviceMonitorGVK = schema.GroupVersionKind{
	Group:   "monitoring.coreos.com",
	Version: "v1",
	Kind:    "ServiceMonitor",
}

// ServiceMonitorForBackend returns an unstructured ServiceMonitor that scrapes
// the proxy sidecar :9090/metrics endpoint in the given namespace.
// Labels match the default kube-prometheus-stack serviceMonitorSelector.
func ServiceMonitorForBackend(namespace, name string) *unstructured.Unstructured {
	sm := &unstructured.Unstructured{}
	sm.SetGroupVersionKind(serviceMonitorGVK)
	sm.SetName(name)
	sm.SetNamespace(namespace)
	sm.SetLabels(map[string]string{
		"app.kubernetes.io/managed-by": "pulse-operator",
		// kube-prometheus-stack default serviceMonitorSelector matches this label.
		"release": "prometheus",
	})

	_ = unstructured.SetNestedStringMap(sm.Object, map[string]string{
		"app.kubernetes.io/name": name,
	}, "spec", "selector", "matchLabels")

	endpoints := []interface{}{
		map[string]interface{}{
			"port":     "pulse-metrics",
			"path":     "/metrics",
			"interval": "30s",
		},
	}
	_ = unstructured.SetNestedSlice(sm.Object, endpoints, "spec", "endpoints")
	_ = unstructured.SetNestedStringSlice(sm.Object, []string{namespace}, "spec", "namespaceSelector", "matchNames")

	return sm
}
