/**
 * Typed client for the Go engine sidecar.
 *
 * Every call funnels through the single `engine_request` Tauri command, which
 * handles JSON-RPC framing and ID correlation on the Rust side.
 *
 * IMPORTANT: The shell emits its first `"ready"` on `engine://status` during
 * Tauri's `setup()`, before the webview bundle has executed — and Tauri neither
 * buffers nor replays events. So on a healthy startup a listener registered from
 * the UI will NEVER see that first `"ready"`. This is by design: readiness is
 * proven by the `health` round-trip, and the event stream is only for later
 * transitions (a crash, a restart, a give-up).
 */
import { invoke } from '@tauri-apps/api/core';
import { listen, type UnlistenFn } from '@tauri-apps/api/event';

export interface Health {
  status: string;
  version: string;
  commit: string;
  pid: number;
}

/** Lifecycle of the sidecar process, pushed by the shell's supervisor. */
export type EngineState = 'ready' | 'restarting' | 'down';

/**
 * One error from the engine seam, with its structure intact.
 *
 * Mirrors `EngineError` in `src-tauri/src/engine.rs`. `code` is the engine's
 * own JSON-RPC code for a reply it produced, or one of {@link EngineErrorCode}
 * for a failure that never reached the engine; `data` is the JSON-RPC `data`
 * member, passed through untouched.
 *
 * Section 11 of the spec requires every error to normalize to
 * `{Kind, Message, Native, Query}`, with the UI branching on `Kind` and
 * `Canceled` deliberately *not* rendering as a failure. None of that is here
 * yet — classification belongs with the driver work that first produces
 * something to classify. This is the shape with room for it: a bare string
 * had nowhere to put `Kind`.
 */
export interface EngineError {
  code: number;
  message: string;
  data?: unknown;
}

/**
 * Codes the shell raises for failures that never reached the engine.
 *
 * JSON-RPC 2.0 reserves -32000..=-32099 for implementation-defined server
 * errors. These mirror the `code` module in `src-tauri/src/engine.rs` — keep
 * the two in sync — except `Ipc`, which only this file can produce.
 */
export const EngineErrorCode = {
  /** No child process: it never started, or has not been replaced yet. */
  Unavailable: -32000,
  /** The write to the child's stdin failed. */
  Transport: -32001,
  /** The child died with the request in flight. */
  Died: -32002,
  /** No reply inside the shell's request timeout. */
  Timeout: -32003,
  /** Unencodable request, or a reply that was not a JSON-RPC response. */
  Malformed: -32004,
  /**
   * The Tauri IPC layer itself failed — an unknown command, an argument the
   * command could not deserialize, or a webview torn down mid-call. Raised
   * only here: it never crosses the Rust seam, because a failure this far
   * out means the seam was never entered.
   */
  Ipc: -32005,
} as const;

/** Narrowing guard for a value that came back across the IPC boundary. */
export function isEngineError(value: unknown): value is EngineError {
  return (
    typeof value === 'object' &&
    value !== null &&
    typeof (value as EngineError).code === 'number' &&
    typeof (value as EngineError).message === 'string'
  );
}

/**
 * Coerces anything thrown across the seam into an {@link EngineError}, so
 * callers can rely on the shape without re-checking it. A value that already
 * is one is returned untouched.
 */
export function toEngineError(value: unknown): EngineError {
  if (isEngineError(value)) return value;
  return {
    code: EngineErrorCode.Ipc,
    message: typeof value === 'string' ? value : String(value),
    data: value,
  };
}

/** Calls one engine method. Rejects with an {@link EngineError}, always. */
export async function request<T>(method: string, params?: unknown): Promise<T> {
  try {
    return await invoke<T>('engine_request', { method, params: params ?? null });
  } catch (err) {
    throw toEngineError(err);
  }
}

export function health(): Promise<Health> {
  return request<Health>('health');
}

export function onStateChange(cb: (state: EngineState) => void): Promise<UnlistenFn> {
  return listen<EngineState>('engine://status', (event) => cb(event.payload));
}
