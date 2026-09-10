import { useEffect, useRef, useState, type KeyboardEvent } from 'react';
import { asDbError, saveConnection, testConnection, type Connection, type NewConnection } from '../lib/connections';
import './ConnectionDialog.css';

export interface ConnectionDialogProps {
  open: boolean;
  onClose: () => void;
  onSaved: (connection: Connection) => void;
}

/**
 * The six connection colours from the Foundations artboard. Colour is a
 * safety feature, not decoration: red is reserved for production, which is
 * why toggling "Production connection" forces it regardless of what the
 * user picked manually.
 */
const SWATCHES = ['#3d7d55', '#24707a', '#4a6ea8', '#7a5aa0', '#a8792c', '#9e4436'] as const;
const DEFAULT_COLOR = '#3d7d55';
const DANGER_COLOR = '#9e4436';

// SQLite is the only selectable driver today (see the segmented control
// below), so this is a constant rather than state. It exists as a named
// value, not inlined, so it is the one place `buildConnection` and
// `missingDriverField` both read from.
const DRIVER = 'sqlite' as const;

type TestStatus = { kind: 'ok' } | { kind: 'error'; message: string } | null;

export function ConnectionDialog({ open, onClose, onSaved }: ConnectionDialogProps) {
  const [name, setName] = useState('');
  const [file, setFile] = useState('');
  const [color, setColor] = useState<string>(DEFAULT_COLOR);
  const [production, setProduction] = useState(false);
  const [testStatus, setTestStatus] = useState<TestStatus>(null);
  const [testing, setTesting] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const nameInputRef = useRef<HTMLInputElement>(null);

  // Nothing else moves focus into a modal that just appeared: without this,
  // Tab from wherever the trigger button was would walk into the sidebar
  // behind the dialog before ever reaching the dialog's own controls.
  useEffect(() => {
    if (open) nameInputRef.current?.focus();
  }, [open]);

  if (!open) return null;

  function buildConnection(): NewConnection {
    return {
      name: name.trim(),
      driver: DRIVER,
      file: file.trim(),
      color,
      read_only: production,
    };
  }

  /**
   * The field this driver cannot dial without, if it is empty — mirrors
   * internal/engine/driver's own RequiredFields on the Go side, so the
   * dialog and the engine agree on what "required" means without one
   * having to trust the other. Unlike Name (checked separately: every
   * driver needs a name to save a record under, dialing does not care what
   * it is called), this is driver-specific. Adding MySQL/MariaDB later is
   * adding a case here, not rewriting this function.
   */
  function missingDriverField(): string | null {
    switch (DRIVER) {
      case 'sqlite':
        return file.trim() ? null : 'File';
    }
  }

  function toggleProduction() {
    setProduction((prev) => {
      const next = !prev;
      setColor(next ? DANGER_COLOR : DEFAULT_COLOR);
      return next;
    });
  }

  async function handleTestConnection() {
    // No guard against re-entry here: the only trigger is the button below,
    // and a disabled <button> never dispatches a click at all — the browser
    // enforces the single-flight invariant, so a redundant check here would
    // be unreachable dead code.
    const missing = missingDriverField();
    if (missing) {
      setFormError(`${missing} is required`);
      setTestStatus(null);
      return;
    }
    setFormError(null);
    setTesting(true);
    setTestStatus(null);
    try {
      const result = await testConnection(buildConnection(), '');
      if (result.ok) {
        setTestStatus({ kind: 'ok' });
      } else {
        setTestStatus({ kind: 'error', message: result.error ?? 'Connection failed' });
      }
    } catch (err) {
      const dbErr = asDbError(err);
      setTestStatus({ kind: 'error', message: dbErr?.message ?? String(err) });
    } finally {
      setTesting(false);
    }
  }

  async function handleConnect() {
    // Unlike Test Connection, this guard is load-bearing: Cmd/Ctrl+Enter
    // reaches this function directly from a keydown handler, which bypasses
    // the Connect button's `disabled` attribute entirely.
    if (saving) return;
    const trimmedName = name.trim();
    if (!trimmedName) {
      setFormError('Name is required');
      return;
    }
    const missing = missingDriverField();
    if (missing) {
      setFormError(`${missing} is required`);
      return;
    }
    setFormError(null);
    setSaving(true);
    try {
      const stored = await saveConnection(buildConnection(), '');
      onSaved(stored);
    } catch (err) {
      const dbErr = asDbError(err);
      setFormError(dbErr?.message ?? String(err));
    } finally {
      setSaving(false);
    }
  }

  function handleKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key === 'Escape') {
      e.stopPropagation();
      onClose();
      return;
    }
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      void handleConnect();
    }
  }

  return (
    <div className="dialog-backdrop">
      <div
        className="dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="connection-dialog-title"
        onKeyDown={handleKeyDown}
      >
        <div className="dialog-header">
          <b id="connection-dialog-title" className="dialog-title">
            New Connection
          </b>
          <button type="button" className="dialog-close" aria-label="Close" onClick={onClose}>
            &#10005;
          </button>
        </div>

        <div className="dialog-body">
          <div className="field">
            <span className="field-label" id="driver-label">
              Driver
            </span>
            <div className="segmented" role="group" aria-labelledby="driver-label">
              <button type="button" disabled aria-pressed="false">
                MySQL
              </button>
              <button type="button" disabled aria-pressed="false">
                MariaDB
              </button>
              <button type="button" aria-pressed="true">
                SQLite
              </button>
            </div>
          </div>

          <div className="field">
            <label className="field-label" htmlFor="conn-name">
              Name<span aria-hidden="true"> *</span>
            </label>
            <input
              id="conn-name"
              ref={nameInputRef}
              className="text-input"
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="local"
              aria-required="true"
              aria-invalid={formError === 'Name is required' ? 'true' : undefined}
            />
          </div>

          <div className="field">
            <label className="field-label" htmlFor="conn-file">
              File<span aria-hidden="true"> *</span>
            </label>
            <input
              id="conn-file"
              className="text-input mono"
              type="text"
              value={file}
              onChange={(e) => setFile(e.target.value)}
              placeholder="/path/to/database.db"
              aria-required="true"
              aria-invalid={formError === 'File is required' ? 'true' : undefined}
            />
          </div>

          <div className="field">
            <span className="field-label" id="colour-label">
              Colour
            </span>
            <div className="swatch-row" role="group" aria-labelledby="colour-label">
              {SWATCHES.map((swatch) => (
                <button
                  key={swatch}
                  type="button"
                  className="swatch"
                  style={{ background: swatch }}
                  aria-pressed={color === swatch}
                  aria-label={`Colour ${swatch}`}
                  onClick={() => setColor(swatch)}
                />
              ))}
              <span className="swatch-hint">Tints the window and every tab</span>
            </div>
          </div>

          <div className={`production-panel${production ? ' is-on' : ''}`}>
            <div className="production-heading">
              <svg viewBox="0 0 16 16" width="13" height="13" aria-hidden="true">
                <path
                  d="M8 2.6l6 10.8H2z"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="1.3"
                  strokeLinejoin="round"
                />
                <path d="M8 6.6v3.1M8 11.5v.1" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
              </svg>
              <b>Production connection</b>
              <span className="spacer" />
              <button
                type="button"
                role="switch"
                aria-checked={production}
                aria-label="Production connection"
                className="toggle"
                onClick={toggleProduction}
              >
                <span className="toggle-thumb" />
              </button>
            </div>
            {production && (
              <p className="production-note">
                Marked red for safety &mdash; new sessions on this connection open read-only.
              </p>
            )}
          </div>

          {formError && (
            <p role="alert" className="field-error">
              {formError}
            </p>
          )}
        </div>

        <div className="dialog-footer">
          <button type="button" className="btn" onClick={() => void handleTestConnection()} disabled={testing}>
            Test Connection
          </button>
          {testStatus?.kind === 'ok' && (
            <span className="test-result ok">
              <span className="result-dot" />
              Reachable
            </span>
          )}
          {testStatus?.kind === 'error' && (
            <span className="test-result error">
              <span className="result-dot" />
              {testStatus.message}
            </span>
          )}
          <span className="spacer" />
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="btn primary" onClick={() => void handleConnect()} disabled={saving}>
            Connect
            <span className="kbd-hint" aria-hidden="true">
              &#8984;&#9166;
            </span>
          </button>
        </div>
      </div>
    </div>
  );
}
