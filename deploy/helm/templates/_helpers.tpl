{{/*
  Standard Helm helper templates. The chart relies on these for object
  naming (so `helm install foo` produces `foo-mqconnector` resources)
  and label selectors (so a Service can find its Pods).
*/}}

{{- define "mqconnector.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "mqconnector.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "mqconnector.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "mqconnector.labels" -}}
app.kubernetes.io/name: {{ include "mqconnector.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "mqconnector.selectorLabels" -}}
app.kubernetes.io/name: {{ include "mqconnector.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "mqconnector.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "mqconnector.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "mqconnector.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{ printf "%s:%s" .Values.image.repository $tag }}
{{- end -}}

{{- define "mqconnector.secretName" -}}
{{- if .Values.secrets.existingSecret -}}
{{- .Values.secrets.existingSecret -}}
{{- else -}}
{{- printf "%s-secrets" (include "mqconnector.fullname" .) -}}
{{- end -}}
{{- end -}}
