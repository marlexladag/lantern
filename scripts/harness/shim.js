/*
 * The Tauri bridge, stood in for.
 *
 * Served same-origin from the harness server and injected as the FIRST
 * element of <head>, ahead of the app bundle, for two reasons. It has to be
 * same-origin because the policy under test says `script-src 'self'` and an
 * inline or foreign script would be blocked — which would make the harness's
 * own probe the first CSP violation it reported. And it has to be first
 * because the listeners below are the whole CSP/error record: anything that
 * fires before they are registered is not merely unreported, it is silently
 * absent from a report that says zero.
 *
 * What it stands in for is `src-tauri/src/engine.rs`, and the passthrough in
 * `invoke` below is deliberately identical to it: an `error` member becomes a
 * rejection carrying {code, message, data} intact, and a result resolves with
 * `?? null`. The UI branches on `data.kind` (spec §11), so a shim that
 * flattened an error to a string would quietly test a different program.
 */
(() => {
  const harness = {
    /** SecurityPolicyViolation events, the reason this file runs first. */
    csp: [],
    errors: [],
    rejections: [],
    /** Every engine call the UI made, in order. */
    rpc: [],
  };
  window.__lanternHarness = harness;

  document.addEventListener('securitypolicyviolation', (e) => {
    harness.csp.push({
      directive: e.effectiveDirective,
      blockedURI: e.blockedURI,
      line: e.lineNumber,
      sample: e.sample,
    });
  });
  window.addEventListener('error', (e) => {
    harness.errors.push(e.message || String(e.error));
  });
  window.addEventListener('unhandledrejection', (e) => {
    const r = e.reason;
    harness.rejections.push(r && r.message ? r.message : JSON.stringify(r));
  });

  let nextId = 1;

  // `isTauri()` is what src/lib/engine.ts checks before it will call invoke
  // at all; without this every request rejects as NoIpc and the app renders
  // its "open me as the desktop app" message instead of anything under test.
  window.isTauri = true;

  window.__TAURI_INTERNALS__ = {
    async invoke(cmd, args) {
      // `listen()` uses this same bridge for `engine://status`. Nothing here
      // emits that event — the harness runs one sidecar and never kills it —
      // so a listener id is all the caller needs back.
      if (cmd !== 'engine_request') return nextId++;

      harness.rpc.push({ method: args.method, params: args.params ?? null });
      const res = await fetch('/__harness/rpc', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ method: args.method, params: args.params ?? null }),
      });
      const frame = await res.json();
      if (frame.error) throw frame.error;
      return frame.result ?? null;
    },
    transformCallback(callback) {
      const id = nextId++;
      Object.defineProperty(window, `_${id}`, { value: callback, configurable: true });
      return id;
    },
    unregisterCallback() {},
  };
})();
