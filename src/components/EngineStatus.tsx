import { useCallback, useEffect, useRef, useState } from 'react';
import {
  EngineErrorCode,
  health,
  onStateChange,
  toEngineError,
  type EngineError,
  type EngineState,
  type Health,
} from '../lib/engine';
import './EngineStatus.css';

type View =
  | { kind: 'connecting' }
  | { kind: 'ready'; info: Health }
  | { kind: 'restarting' }
  | { kind: 'down' }
  | { kind: 'error'; error: EngineError };

/**
 * The Kind badge design/States.dc.html's error-panel treatment puts beside
 * the plain-language message. Section 11's full `Kind` classification is
 * driver work that has not landed yet (see the comment on `EngineError`
 * above) — this only distinguishes the one case the UI can act on
 * specifically (open the desktop app) from everything else, which is
 * still an engine-seam failure rather than a raw exception.
 */
function engineErrorKind(error: EngineError): string {
  return error.code === EngineErrorCode.NoIpc ? 'DESKTOP' : 'ENGINE';
}

export function EngineStatus() {
  const [view, setView] = useState<View>({ kind: 'connecting' });
  // Guards against a stale handshake resolving after a newer one: if the
  // engine flaps (restarting -> ready -> restarting -> ready) fast enough,
  // two health() calls can be in flight at once, and network/IPC timing
  // gives no guarantee the older one settles first. Only the result whose
  // generation still matches the latest dispatched handshake is applied.
  // A restarting/down transition also bumps this even though it does not
  // dispatch a handshake itself, so a handshake still in flight from
  // *before* that transition can never land afterward and overwrite it.
  const generation = useRef(0);

  const handshake = useCallback(async () => {
    const gen = ++generation.current;
    setView({ kind: 'connecting' });
    try {
      const info = await health();
      if (gen === generation.current) setView({ kind: 'ready', info });
    } catch (err) {
      // request() already guarantees an EngineError; normalizing again is
      // cheap and keeps this component correct if some other throw ever
      // reaches here.
      if (gen === generation.current) setView({ kind: 'error', error: toEngineError(err) });
    }
  }, []);

  useEffect(() => {
    void handshake();
  }, [handshake]);

  useEffect(() => {
    let unlisten: (() => void) | undefined;
    let cancelled = false;

    void onStateChange((state: EngineState) => {
      if (state === 'restarting') {
        generation.current++;
        setView({ kind: 'restarting' });
      } else if (state === 'ready') {
        // A fresh process means a fresh PID, so re-handshake rather than
        // trusting the values from the process that just died.
        void handshake();
      } else {
        generation.current++;
        setView({ kind: 'down' });
      }
    }).then((fn) => {
      if (cancelled) fn();
      else unlisten = fn;
    });

    return () => {
      cancelled = true;
      unlisten?.();
    };
  }, [handshake]);

  switch (view.kind) {
    case 'connecting':
      return <p>Connecting to engine…</p>;
    case 'restarting':
      return <p>Engine restarting…</p>;
    case 'down':
      return (
        <p role="alert">
          Engine is down. It failed to restart after repeated attempts and needs manual attention.
        </p>
      );
    case 'error':
      // Where Section 11's `Kind` branch will go: today every error renders
      // the same way, but `Canceled` must NOT paint an alert once queries
      // can be stopped. Branch on view.error.code (or the Kind that will
      // sit beside it) here rather than adding a second error path.
      //
      // Rendered with design/States.dc.html's error-panel treatment — a
      // Kind badge, the plain-language message leading, and the numeric
      // code as native detail available but not leading — so a shell
      // failure never again reaches the user as a raw exception (see
      // engineErrorKind above and the NoIpc guard in lib/engine.ts).
      return (
        <p role="alert" className="engine-error">
          <span className="engine-error-badge">{engineErrorKind(view.error)}</span>
          <span className="engine-error-message">{view.error.message}</span>
          <span className="engine-error-detail">code {view.error.code}</span>
        </p>
      );
    case 'ready':
      return (
        <p>
          Engine {view.info.version} ({view.info.commit}) — pid {view.info.pid}
        </p>
      );
  }
}
