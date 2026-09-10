import { useCallback, useEffect, useRef, useState } from 'react';
import { health, onStateChange, type EngineState, type Health } from '../lib/engine';

type View =
  | { kind: 'connecting' }
  | { kind: 'ready'; info: Health }
  | { kind: 'restarting' }
  | { kind: 'error'; message: string };

export function EngineStatus() {
  const [view, setView] = useState<View>({ kind: 'connecting' });
  // Guards against a stale handshake resolving after a newer one: if the
  // engine flaps (restarting -> ready -> restarting -> ready) fast enough,
  // two health() calls can be in flight at once, and network/IPC timing
  // gives no guarantee the older one settles first. Only the result whose
  // generation still matches the latest dispatched handshake is applied.
  const generation = useRef(0);

  const handshake = useCallback(async () => {
    const gen = ++generation.current;
    setView({ kind: 'connecting' });
    try {
      const info = await health();
      if (gen === generation.current) setView({ kind: 'ready', info });
    } catch (err) {
      if (gen === generation.current) setView({ kind: 'error', message: String(err) });
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
        setView({ kind: 'restarting' });
      } else if (state === 'ready') {
        // A fresh process means a fresh PID, so re-handshake rather than
        // trusting the values from the process that just died.
        void handshake();
      } else {
        setView({ kind: 'error', message: 'Engine is down.' });
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
    case 'error':
      return <p role="alert">Engine error: {view.message}</p>;
    case 'ready':
      return (
        <p>
          Engine {view.info.version} ({view.info.commit}) — pid {view.info.pid}
        </p>
      );
  }
}
