{{/*
  _validate.tpl — install-time guards.

  Helm has no native "preconditions" mechanism, but a template that
  unconditionally calls `fail` aborts the install with a clean error
  before any resources are created. We use that to surface the most
  painful misconfigurations early — at `helm install`/`helm upgrade`
  time rather than as a downstream CrashLoopBackOff.

  Every guard runs on every render; the chart's other templates pull
  this file in via `{{ include "mqconnector.validate" . }}` (no-op
  output, side-effect-only).
*/}}
{{- define "mqconnector.validate" -}}

{{- /*
  Guard 1: prod mode requires a master key.

  Phase 1 hardening: in prod mode the binary refuses to start unless
  MQC_MASTER_KEY (or MQC_MASTER_KEYS) is set, so an unconfigured
  install would CrashLoopBackOff with a startup error. Surface the
  requirement here so the operator sees it during `helm install`.

  Accepted forms (any ONE satisfies the guard):
    - values.secrets.masterKey       — single-version key (hex / b64)
    - values.secrets.masterKeys      — multi-version rotation string
    - values.secrets.existingSecret  — operator-managed Secret with
                                       MQC_MASTER_KEY[S] inside

  The check is skipped when mode=dev — dev clusters routinely run
  without encryption at rest.
*/ -}}
{{- $mode := (default "prod" .Values.config.server.mode) -}}
{{- if eq $mode "prod" -}}
  {{- $hasKey := false -}}
  {{- if .Values.secrets.existingSecret -}}{{- $hasKey = true -}}{{- end -}}
  {{- if .Values.secrets.masterKey -}}{{- $hasKey = true -}}{{- end -}}
  {{- if .Values.secrets.masterKeys -}}{{- $hasKey = true -}}{{- end -}}
  {{- if not $hasKey -}}
    {{- fail (printf "\n\n  mqConnector: server.mode=prod requires a master key for at-rest secret encryption.\n  Set one of:\n    secrets.masterKey       (single hex/base64 32-byte key, e.g. `openssl rand -hex 32`)\n    secrets.masterKeys      (multi-version: \"v1=...,v2=...\")\n    secrets.existingSecret  (name of a Secret holding MQC_MASTER_KEY or MQC_MASTER_KEYS)\n  Or flip config.server.mode=dev for local clusters.\n") -}}
  {{- end -}}
{{- end -}}

{{- /*
  Guard 2: multi-replica requires the leadership lease.

  Without it, both replicas would consume the same source queue and
  double-deliver every message. Storage layer can't catch this — the
  brokers see two independent consumers and behave accordingly.
*/ -}}
{{- if and (gt (int .Values.replicaCount) 1) (not .Values.config.leadership.enabled) -}}
  {{- fail (printf "\n\n  mqConnector: replicaCount=%d requires config.leadership.enabled=true so only one replica drains the source queue.\n  Set config.leadership.enabled=true (recommended TTL: 30s) or drop replicaCount to 1.\n" (int .Values.replicaCount)) -}}
{{- end -}}

{{- /*
  Guard 3: prod mode forbids auth.insecure_skip_verify.

  Mirrors the binary's own Config.Validate() so the failure surfaces
  at install time, not at pod startup.
*/ -}}
{{- if and (eq $mode "prod") .Values.config.auth.insecureSkipVerify -}}
  {{- fail "\n\n  mqConnector: config.auth.insecureSkipVerify=true is only allowed in dev mode. Either set config.server.mode=dev or fix the SimpleAuth certificate chain.\n" -}}
{{- end -}}

{{- end -}}
