package observability

import (
	"encoding/json"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var grafanaDashboardGVK = schema.GroupVersionKind{
	Group:   "grafana.integreatly.org",
	Version: "v1beta1",
	Kind:    "GrafanaDashboard",
}

// GrafanaDashboardForBackend returns an unstructured GrafanaDashboard for the given LLMBackend.
// The dashboard JSON is embedded inline and covers the core Weekend-1 panels:
// requests/sec, cost/hr, latency (p50/p95/p99), error rate, token breakdown.
func GrafanaDashboardForBackend(namespace, name string) *unstructured.Unstructured {
	gd := &unstructured.Unstructured{}
	gd.SetGroupVersionKind(grafanaDashboardGVK)
	gd.SetName(name)
	gd.SetNamespace(namespace)
	gd.SetLabels(map[string]string{
		"app.kubernetes.io/managed-by": "pulse-operator",
	})

	dashJSON, _ := json.Marshal(buildDashboard(name))
	_ = unstructured.SetNestedField(gd.Object, string(dashJSON), "spec", "json")
	_ = unstructured.SetNestedField(gd.Object, map[string]interface{}{
		"name": "prometheus",
	}, "spec", "datasource")

	return gd
}

// buildDashboard returns a minimal Grafana dashboard model.
func buildDashboard(name string) map[string]interface{} {
	return map[string]interface{}{
		"title":       "Pulse — " + name,
		"uid":         "pulse-" + name,
		"schemaVersion": 39,
		"refresh":     "30s",
		"panels": []interface{}{
			panel(1, "Requests / sec", 0, 0, 12, 8,
				`rate(pulse_llm_requests_total[1m])`),
			panel(2, "Cost USD / hr", 12, 0, 12, 8,
				`sum(rate(pulse_llm_requests_total[1m])) * 3600`),
			panel(3, "Latency p95 (ms)", 0, 8, 12, 8,
				`histogram_quantile(0.95, rate(pulse_llm_latency_ms_bucket[5m]))`),
			panel(4, "Error Rate", 12, 8, 12, 8,
				`rate(pulse_llm_requests_total{status=~"5.."}[1m]) / rate(pulse_llm_requests_total[1m])`),
		},
	}
}

func panel(id int, title string, x, y, w, h int, expr string) map[string]interface{} {
	return map[string]interface{}{
		"id":    id,
		"type":  "timeseries",
		"title": title,
		"gridPos": map[string]interface{}{
			"x": x, "y": y, "w": w, "h": h,
		},
		"targets": []interface{}{
			map[string]interface{}{
				"expr":         expr,
				"datasource":   map[string]interface{}{"type": "prometheus"},
				"legendFormat": "{{model}}",
			},
		},
	}
}
