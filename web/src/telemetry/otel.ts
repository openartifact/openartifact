// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// The OpenTelemetry browser SDK behind one module, imported only by ./index.ts
// once the server reports frontend telemetry on. Experimental: the packages
// are pinned to exact versions in package.json, and a bump may change the
// data model. Scope is errors, web vitals and one client span per API call
// (web/src/api/client.ts); no console, user-action, document-load or
// navigation instrumentation, and the deprecated fetch/XHR instrumentations
// are not used. Nothing is registered globally: the providers are handed to
// the instrumentations and to client.ts directly, and nothing is stored in
// cookies or local storage.

import { SeverityNumber, SpanKind, SpanStatusCode, type Attributes } from '@opentelemetry/api'
import { ErrorsInstrumentation } from '@opentelemetry/browser-instrumentation/experimental/errors'
import { WebVitalsInstrumentation } from '@opentelemetry/browser-instrumentation/experimental/web-vitals'
import { OTLPLogExporter } from '@opentelemetry/exporter-logs-otlp-proto'
import { OTLPTraceExporter } from '@opentelemetry/exporter-trace-otlp-proto'
import { resourceFromAttributes } from '@opentelemetry/resources'
import { BatchLogRecordProcessor, LoggerProvider } from '@opentelemetry/sdk-logs'
import { BatchSpanProcessor, ParentBasedSampler, TraceIdRatioBasedSampler, TracerProvider } from '@opentelemetry/sdk-trace'
import { version } from '../../package.json'
import { csrfHeader, type ClientSpan } from '../api/client'
import type { FrontendTelemetryConfig } from '../api/telemetry'

export const SERVICE_NAME = 'openartifact-web'

/** Same-origin relay endpoints: the session cookie and the CSRF header authenticate the export. */
const RELAY = { traces: '/api/v1/telemetry/traces', logs: '/api/v1/telemetry/logs' } as const

/** Records per export request, well below the relay's 1 MiB body cap even with long stack traces. */
const MAX_EXPORT_BATCH = 64

const HTTP_METHODS = new Set(['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS', 'CONNECT', 'TRACE'])

/** What ./index.ts wires into the app. */
export interface Telemetry {
  startClientSpan(method: string): ClientSpan
  recordError(err: unknown): void
  /** Stops capturing and shuts both providers down; the final export is best effort (the session may already be gone). */
  shutdown(): Promise<void>
}

/** The headers of every export: read per request, so a CSRF token rotated by a new sign-in is picked up. */
async function exportHeaders(): Promise<Record<string, string>> {
  return { 'X-Requested-With': 'XMLHttpRequest', ...csrfHeader() }
}

function relayURL(path: string): string {
  return new URL(path, window.location.origin).href
}

/** The `exception` log event attributes of err, as the errors instrumentation records them. */
export function exceptionAttributes(err: unknown): Attributes {
  if (!(err instanceof Error)) return { 'exception.message': String(err) }
  const attrs: Attributes = { 'exception.type': err.name, 'exception.message': err.message }
  if (err.stack) attrs['exception.stacktrace'] = err.stack
  return attrs
}

/** W3C traceparent (version 00) of a span context. */
export function traceparent(ctx: { traceId: string; spanId: string; traceFlags: number }): string {
  return `00-${ctx.traceId}-${ctx.spanId}-${ctx.traceFlags.toString(16).padStart(2, '0')}`
}

/** Starts the SDK with the server's settings. Called once per page by ./index.ts. */
export function initTelemetry(settings: FrontendTelemetryConfig): Telemetry {
  const resource = resourceFromAttributes({ 'service.name': SERVICE_NAME, 'service.version': version })

  // Parent based on the server's ratio: client spans are roots, and their
  // sampled flag travels in traceparent, so the server records exactly the
  // calls the UI recorded.
  const tracerProvider = new TracerProvider({
    resource,
    sampler: new ParentBasedSampler({ root: new TraceIdRatioBasedSampler(settings.sample_ratio) }),
    spanProcessors: [
      new BatchSpanProcessor({
        exporter: new OTLPTraceExporter({ url: relayURL(RELAY.traces), headers: exportHeaders }),
        maxExportBatchSize: MAX_EXPORT_BATCH,
      }),
    ],
  })
  const loggerProvider = new LoggerProvider({
    resource,
    processors: [
      new BatchLogRecordProcessor({
        exporter: new OTLPLogExporter({ url: relayURL(RELAY.logs), headers: exportHeaders }),
        maxExportBatchSize: MAX_EXPORT_BATCH,
      }),
    ],
  })

  // Unhandled errors and rejections as `exception` events, Core Web Vitals as
  // `browser.web_vital` events. Created disabled so nothing is captured
  // before they write to our provider instead of the (no-op) global one.
  const instrumentations = [new ErrorsInstrumentation({ enabled: false }), new WebVitalsInstrumentation({ enabled: false })]
  for (const instrumentation of instrumentations) {
    instrumentation.setLoggerProvider(loggerProvider)
    instrumentation.enable()
  }

  const tracer = tracerProvider.getTracer(SERVICE_NAME, version)
  const logger = loggerProvider.getLogger(SERVICE_NAME, version)

  return {
    startClientSpan(method) {
      const m = HTTP_METHODS.has(method.toUpperCase()) ? method.toUpperCase() : '_OTHER'
      // Attributes follow internal/telemetry: no URL, path or query (they can
      // carry tokens and user data); the server span has the route template.
      const span = tracer.startSpan(`HTTP ${m}`, { kind: SpanKind.CLIENT, attributes: { 'http.request.method': m } })
      return {
        traceparent: traceparent(span.spanContext()),
        end(status) {
          span.setAttribute('http.response.status_code', status)
          if (status >= 400) {
            span.setAttribute('error.type', String(status))
            span.setStatus({ code: SpanStatusCode.ERROR })
          }
          span.end()
        },
        fail(err) {
          span.setAttribute('error.type', err instanceof Error ? err.name : '_OTHER')
          span.setStatus({ code: SpanStatusCode.ERROR })
          span.end()
        },
      }
    },
    recordError(err) {
      logger.emit({ eventName: 'exception', severityNumber: SeverityNumber.ERROR, attributes: exceptionAttributes(err) })
    },
    async shutdown() {
      for (const instrumentation of instrumentations) instrumentation.disable()
      await Promise.allSettled([tracerProvider.shutdown(), loggerProvider.shutdown()])
    },
  }
}
