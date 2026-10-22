{{- /* The chart's name in resource names and labels. A release installed before the rename sets
nameOverride=popcorn, so its Deployment selector and volume claim keep their names. */ -}}
{{- define "cheapseats.name" -}}
{{- .Values.nameOverride | default .Chart.Name -}}
{{- end -}}

{{- define "cheapseats.fullname" -}}
{{- $name := include "cheapseats.name" . -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "cheapseats.labels" -}}
app.kubernetes.io/name: {{ include "cheapseats.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Values.image.tag | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "cheapseats.selector" -}}
app.kubernetes.io/name: {{ include "cheapseats.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "cheapseats.claim" -}}
{{- .Values.persistence.existingClaim | default (printf "%s-data" (include "cheapseats.fullname" .)) -}}
{{- end -}}
