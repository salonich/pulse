{{/*
Expand the name of the chart.
*/}}
{{- define "pulse.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "pulse.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels applied to all resources.
*/}}
{{- define "pulse.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | trunc 63 }}
app.kubernetes.io/name: {{ include "pulse.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "pulse.selectorLabels" -}}
app.kubernetes.io/name: {{ include "pulse.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Namespace for Pulse system components.
*/}}
{{- define "pulse.namespace" -}}
{{- default "pulse-system" .Values.namespaceOverride }}
{{- end }}

{{/*
Operator image reference.
*/}}
{{- define "pulse.operatorImage" -}}
{{- with .Values.global.imageRegistry }}{{ . }}/{{ end -}}
{{- .Values.operator.image.repository }}:{{ .Values.operator.image.tag }}
{{- end }}
