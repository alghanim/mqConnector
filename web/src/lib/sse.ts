// Server-Sent Events client for /api/v1/events.
//
// Wraps the browser's EventSource with three small contracts the rest of
// the app relies on:
//
//   1. Typed event dispatch — callers register handlers per event name
//      (`hello`, `metrics`, `dlq_total`, `health`) and the payload is JSON
//      parsed before the handler sees it.
//
//   2. Reconnect with backoff — EventSource has built-in reconnect but
//      it's noisy. We give it a 1s → 30s exponential ramp on hard errors
//      and a hard ceiling on consecutive failures before falling back to
//      polling, which the caller can detect via the `onFallback` callback.
//
//   3. Lifecycle hooks — `start()` / `stop()` so the dashboard can tear
//      the stream down on unmount and stop the fallback poller too.
//
// Auth is the existing cookie session — EventSource sends cookies same as
// fetch, no extra wiring needed.

type Handler<T> = (data: T) => void;

export interface SSEHelloPayload {
  interval_ms: number;
  heartbeat_ms: number;
  server_time: string;
}

export interface SSEMetricsPayload<P = unknown> {
  uptime: string;
  pipelines: Record<string, P>;
  active: number;
}

export interface SSEDLQPayload {
  total: number;
}

export interface SSEHealthPayload {
  status: string;
  active_pipelines: number;
  version: string;
  uptime: string;
  connections?: Array<{
    pipeline_id: string;
    status: string;
    source_queue: string;
    dest_queue: string;
    last_error?: string;
  }>;
}

export interface SSEOptions {
  /** Query string parameters appended to the URL. */
  params?: Record<string, string | number>;
  /** Called the first time we give up and switch to polling. */
  onFallback?: () => void;
  /** Called when the stream is healthy again after a transient error. */
  onResume?: () => void;
  /** Maximum consecutive errors before we surrender to polling. */
  maxConsecutiveErrors?: number;
}

const DEFAULT_MAX_ERRORS = 5;

export class SSEClient {
  private url: string;
  private es: EventSource | null = null;
  private opts: SSEOptions;
  private stopped = false;
  private consecutiveErrors = 0;
  private fellBack = false;
  private handlers = new Map<string, Handler<unknown>>();
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;

  constructor(path: string, opts: SSEOptions = {}) {
    const qs = new URLSearchParams();
    for (const [k, v] of Object.entries(opts.params ?? {})) qs.set(k, String(v));
    const tail = qs.toString();
    this.url = tail ? `${path}?${tail}` : path;
    this.opts = opts;
  }

  on<T = unknown>(event: string, fn: Handler<T>): this {
    this.handlers.set(event, fn as Handler<unknown>);
    return this;
  }

  start(): this {
    this.stopped = false;
    this.open();
    return this;
  }

  stop(): void {
    this.stopped = true;
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.es) {
      this.es.close();
      this.es = null;
    }
  }

  private open() {
    if (this.stopped || typeof EventSource === 'undefined') return;

    try {
      this.es = new EventSource(this.url, { withCredentials: true });
    } catch {
      this.scheduleReconnect();
      return;
    }

    this.es.onopen = () => {
      if (this.consecutiveErrors > 0 && this.opts.onResume) {
        this.opts.onResume();
      }
      this.consecutiveErrors = 0;
    };

    this.es.onerror = () => {
      // EventSource auto-reconnects, but we want to give up after a few
      // hard failures so the caller can drop to polling. CLOSED means
      // the browser stopped trying.
      this.consecutiveErrors++;
      const limit = this.opts.maxConsecutiveErrors ?? DEFAULT_MAX_ERRORS;
      if (this.consecutiveErrors >= limit && !this.fellBack) {
        this.fellBack = true;
        this.stop();
        this.opts.onFallback?.();
        return;
      }
      if (this.es && this.es.readyState === EventSource.CLOSED) {
        this.es = null;
        this.scheduleReconnect();
      }
    };

    // Register typed listeners for every handler the caller registered.
    for (const [event, fn] of this.handlers.entries()) {
      this.es.addEventListener(event, (ev: MessageEvent) => {
        let payload: unknown = ev.data;
        try {
          payload = JSON.parse(ev.data);
        } catch {
          // leave raw string — caller can decide what to do
        }
        fn(payload);
      });
    }
  }

  private scheduleReconnect() {
    if (this.stopped) return;
    // 1s, 2s, 4s, 8s, 16s, capped at 30s. The browser's built-in
    // reconnect is opaque; we replace it here so we can keep state.
    const backoff = Math.min(30_000, 1000 * 2 ** Math.min(5, this.consecutiveErrors));
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      this.open();
    }, backoff);
  }
}

/**
 * Convenience factory for the operations event stream. Pass `interval` in
 * milliseconds to ask for a slower cadence (clamped server-side to
 * 500..30000). `onFallback` fires once if the stream gives up and the
 * caller should drop back to polling; `onResume` fires when the stream
 * recovers after a transient error.
 */
export function openEventsStream(
  intervalMs?: number,
  callbacks: { onFallback?: () => void; onResume?: () => void } = {}
): SSEClient {
  const opts: SSEOptions = { ...callbacks };
  if (intervalMs) opts.params = { interval: intervalMs };
  return new SSEClient('/api/v1/events', opts);
}
