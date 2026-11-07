{{- define "kourrier.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "kourrier.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "kourrier.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "kourrier.labels" -}}
helm.sh/chart: {{ include "kourrier.chart" . }}
{{ include "kourrier.selectorLabels" . }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "kourrier.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kourrier.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "kourrier.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kourrier.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "kourrier.secretName" -}}
{{- default (include "kourrier.fullname" .) .Values.secrets.existingSecret }}
{{- end }}

{{- define "kourrier.runsAPI" -}}
{{- if or (eq .Values.mode "all") (eq .Values.mode "api") }}true{{ end }}
{{- end }}

{{- define "kourrier.routesConfigMap" -}}
{{- default (printf "%s-routes" (include "kourrier.fullname" .)) .Values.events.existingConfigMap }}
{{- end }}

{{/* The event ingress runs with the API, when routes are configured. */}}
{{- define "kourrier.eventsEnabled" -}}
{{- if and (include "kourrier.runsAPI" .) (or .Values.events.routes .Values.events.existingConfigMap) }}true{{ end }}
{{- end }}
