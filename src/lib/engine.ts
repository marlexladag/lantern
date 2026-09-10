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

/** Calls one engine method. Rejects with the engine's error message. */
export async function request<T>(method: string, params?: unknown): Promise<T> {
  return invoke<T>('engine_request', { method, params: params ?? null });
}

export function health(): Promise<Health> {
  return request<Health>('health');
}

export function onStateChange(cb: (state: EngineState) => void): Promise<UnlistenFn> {
  return listen<EngineState>('engine://status', (event) => cb(event.payload));
}
