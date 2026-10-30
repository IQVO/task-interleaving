{{/*
Expand the name of the chart.
*/}}
{{- define "task-interleaving.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "task-interleaving.fullname" -}}
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

{{/*
Chart name and version as used by the chart label.
*/}}
{{- define "task-interleaving.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "task-interleaving.labels" -}}
helm.sh/chart: {{ include "task-interleaving.chart" . }}
{{ include "task-interleaving.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels. The Deployment and the Service ALSO pin
app.kubernetes.io/component (api), so a Service never selects more than the
pods of one component (tests/test_service_selectors.py).
*/}}
{{- define "task-interleaving.selectorLabels" -}}
app.kubernetes.io/name: {{ include "task-interleaving.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "task-interleaving.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "task-interleaving.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Name of the Secret holding DATABASE_URL: the operator's own
(database.existingSecret) or the one this chart creates from database.url.
*/}}
{{- define "task-interleaving.databaseSecretName" -}}
{{- if .Values.database.existingSecret }}
{{- .Values.database.existingSecret }}
{{- else }}
{{- include "task-interleaving.fullname" . }}-database
{{- end }}
{{- end }}

{{/*
Image reference.
*/}}
{{- define "task-interleaving.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{/*
MANDATORY (ADR 0006: Postgres is the only runtime). The binary exits at boot
without DATABASE_URL, so a render without a database source is never a
deployable state: surface it at render time, naming the values.
*/}}
{{- define "task-interleaving.requireDatabase" -}}
{{- if not (or .Values.database.url .Values.database.existingSecret) -}}
{{- fail "task-interleaving requires database.url or database.existingSecret to be set (DATABASE_URL): the service runs on real Postgres only and exits at boot without it (ADR 0006)." -}}
{{- end -}}
{{- end -}}

{{/*
MANDATORY (ADR 0006: Kafka is the only runtime). The binary exits at boot
without KAFKA_BROKERS; there is no default broker.
*/}}
{{- define "task-interleaving.requireKafka" -}}
{{- $_ := required "task-interleaving requires kafka.brokers to be set (KAFKA_BROKERS, comma-separated): the service runs on real Kafka only and exits at boot without it (ADR 0006)." .Values.kafka.brokers -}}
{{- end -}}

{{/*
MANDATORY: the two consumers always run, each with a STABLE group id read
from the environment (cmd/api planConsumers exits at boot without one). The
group ids live in values (config.*ConsumerGroup), never in a template literal.
*/}}
{{- define "task-interleaving.requireConsumerGroups" -}}
{{- $_ := required "task-interleaving requires config.fulfillmentConsumerGroup to be set (FULFILLMENT_CONSUMER_GROUP): a stable Kafka group id; the binary exits at boot without it." .Values.config.fulfillmentConsumerGroup -}}
{{- $_ := required "task-interleaving requires config.layoutConsumerGroup to be set (LAYOUT_CONSUMER_GROUP): a stable Kafka group id; the binary exits at boot without it." .Values.config.layoutConsumerGroup -}}
{{- end -}}

{{/*
Shutdown is sequential (cmd/api serveUntilSignal stops and awaits the workers
one by one, each bounded by 10s): SHUTDOWN_DRAIN_DELAY (default 5s) + HTTP
drain 10s + 2 consumers 2 x 10s + hint-expiry worker 10s + outbox relay 10s =
55s in the worst case. A shorter grace period gets the pod SIGKILLed
mid-drain.
*/}}
{{- define "task-interleaving.requireGracePeriod" -}}
{{- if lt (int .Values.terminationGracePeriodSeconds) 55 -}}
{{- fail (printf "terminationGracePeriodSeconds is %v — want at least 55: SHUTDOWN_DRAIN_DELAY (5s default) + 10s HTTP drain + 2 x 10s consumers + 10s hint-expiry worker + 10s outbox relay." .Values.terminationGracePeriodSeconds) -}}
{{- end -}}
{{- end -}}

{{/*
The *_MODE variables (FULFILLMENT_MODE, LAYOUT_MODE) default to
kafka and the binary rejects "permissive" at boot (ADR 0006): they must never
be injected through extraEnv either.
*/}}
{{- define "task-interleaving.requireNoModeEnv" -}}
{{- range .Values.extraEnv -}}
{{- if hasSuffix "_MODE" (toString .name) -}}
{{- fail (printf "extraEnv sets %s — the *_MODE variables are not configurable: the only mode is kafka and the binary rejects permissive at boot (ADR 0006)." .name) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Comma-joined names of this chart's enabled KongPlugins, for the
konghq.com/plugins annotation on the HTTPRoute this chart renders.
Empty string (annotation omitted) when none are enabled.
*/}}
{{- define "task-interleaving.kongPluginNames" -}}
{{- $names := list -}}
{{- range $name, $cfg := .Values.kongPlugins -}}
{{- if $cfg.enabled -}}
{{- $names = append $names (printf "%s-%s" (include "task-interleaving.fullname" $) $name) -}}
{{- end -}}
{{- end -}}
{{- join "," $names -}}
{{- end -}}

{{/*
Fully qualified names of the optional workloads. Each is its own Deployment +
Service pair, pinned by app.kubernetes.io/component (tests/test_service_selectors.py).
*/}}
{{- define "task-interleaving.mcpFullname" -}}
{{- include "task-interleaving.fullname" . }}-mcp
{{- end }}

{{- define "task-interleaving.a2aFullname" -}}
{{- include "task-interleaving.fullname" . }}-a2a
{{- end }}

{{- define "task-interleaving.analyticsProjectorFullname" -}}
{{- include "task-interleaving.fullname" . }}-analytics-projector
{{- end }}

{{- define "task-interleaving.analyticsReportsFullname" -}}
{{- include "task-interleaving.fullname" . }}-analytics-reports
{{- end }}

{{/*
Name of the Secret holding ANALYTICS_DATABASE_URL: the operator's own
(analytics.existingSecret) or the one this chart creates from analytics.url.
*/}}
{{- define "task-interleaving.analyticsSecretName" -}}
{{- if .Values.analytics.existingSecret -}}
{{- .Values.analytics.existingSecret -}}
{{- else -}}
{{- include "task-interleaving.fullname" . }}-analytics
{{- end -}}
{{- end }}

{{/*
analytics.enabled needs the analytical DSN: ti-projector and ti-reports exit at
boot without ANALYTICS_DATABASE_URL (ADR 0008). Kafka brokers are mandatory for
the whole chart already (requireKafka), which the projector reuses.
*/}}
{{- define "task-interleaving.requireAnalyticsConfig" -}}
{{- if not (or .Values.analytics.url .Values.analytics.existingSecret) -}}
{{- fail "analytics.enabled is true but neither analytics.url nor analytics.existingSecret is set — ti-projector and ti-reports exit at boot without ANALYTICS_DATABASE_URL (ADR 0008)." -}}
{{- end -}}
{{- if not .Values.analytics.consumerGroup -}}
{{- fail "analytics.enabled is true but analytics.consumerGroup is empty — ti-projector needs a stable ANALYTICS_CONSUMER_GROUP so a rescheduled pod resumes from its committed offsets (ADR 0008)." -}}
{{- end -}}
{{- end -}}
