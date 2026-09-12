import { useEffect, useRef, useState, type KeyboardEvent } from 'react';
import {
  listDrivers, saveConnection, testConnection,
  type Connection, type DriverInfo, type NewConnection,
} from '../lib/connections';
import { describeError, type ErrorDescription } from '../lib/errors';
import { ErrorText } from './ErrorText';
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

/**
 * Display names for driver ids whose conventional capitalization the id
 * itself cannot carry. Cosmetic only, and entirely optional: a driver with
 * no entry is shown under its own id, so registering one in Go puts it in
 * this picker without anybody editing this file. That is the point of the
 * whole exercise — an entry here is a nicety, not a registration.
 */
const DRIVER_LABELS: Record<string, string> = { sqlite: 'SQLite' };

/**
 * Placeholders for fields we happen to know a good example for. Same rule as
 * DRIVER_LABELS: a field with no entry simply renders without one.
 */
const FIELD_PLACEHOLDERS: Record<string, string> = { file: '/path/to/database.db' };

/**
 * A field's label from the engine's name for it.
 *
 * The engine names fields by their lowercase ConnConfig field name, and this
 * is the same transformation `checkConnectionIsSavable` applies in Go before
 * naming one in a refusal — so whichever side refuses the save, the user
 * reads the same word.
 */
function fieldLabel(field: string): string {
  return field.charAt(0).toUpperCase() + field.slice(1);
}

// Every control a keyboard user can land on inside the dialog, in DOM
// order — a disabled control (Connect and Test Connection, while the driver
// list is missing) is excluded automatically. Used both to seed focus on
// open and to trap Tab at the two ends so it wraps within the dialog instead
// of escaping into the sidebar behind it.
const FOCUSABLE_SELECTOR =
  'button:not([disabled]), input:not([disabled]), [href], select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

type TestStatus = { kind: 'ok' } | { kind: 'error'; error: ErrorDescription } | null;

export function ConnectionDialog({ open, onClose, onSaved }: ConnectionDialogProps) {
  const [name, setName] = useState('');
  /**
   * The engine's driver list, and the id picked out of it. Empty until
   * drivers.list answers: this dialog has no idea what drivers exist, which
   * is why adding one no longer means editing it.
   */
  const [drivers, setDrivers] = useState<DriverInfo[]>([]);
  const [driverId, setDriverId] = useState('');
  const [driversError, setDriversError] = useState<ErrorDescription | null>(null);
  /**
   * What the user has typed, keyed by the ENGINE's name for the field. The
   * form has no fixed fields of its own, so neither does this.
   */
  const [values, setValues] = useState<Record<string, string>>({});
  /**
   * The fields the last refusal named, so each one can mark itself invalid.
   * Separate from `formError` because one refusal can name several fields and
   * matching on the rendered sentence to find out which is a trick that
   * breaks the moment the sentence changes.
   */
  const [invalid, setInvalid] = useState<string[]>([]);
  const [color, setColor] = useState<string>(DEFAULT_COLOR);
  const [production, setProduction] = useState(false);
  const [testStatus, setTestStatus] = useState<TestStatus>(null);
  const [testing, setTesting] = useState(false);
  const [formError, setFormError] = useState<ErrorDescription | null>(null);
  const [saving, setSaving] = useState(false);
  const nameInputRef = useRef<HTMLInputElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  // The element focused right before the dialog opened — restored on every
  // close path (Escape, Cancel, ×, or a successful save all funnel through
  // `open` going back to false) so a keyboard user lands back where they
  // were instead of at <body>.
  const openerRef = useRef<HTMLElement | null>(null);
  /**
   * Guards against a request abandoned by a close landing on the form that
   * replaced it. Reported shape: type a path, click Test Connection, press
   * Escape while it is in flight, reopen — the form is correctly blank, and
   * then the abandoned request resolves and plants "Reachable" on it, a
   * verdict asserting reachability about a file being replaced. Resetting on
   * open cannot help: the result arrives after the reset.
   *
   * Each handler captures this counter before awaiting and compares after;
   * the open/close effect bumps it, so anything dispatched before the
   * boundary is dropped on the other side of it. Same mechanism, same shape,
   * as EngineStatus's `generation` — a second pattern for the same problem
   * would be one more thing to keep in step.
   */
  const generation = useRef(0);

  useEffect(() => {
    // App.tsx keeps this component mounted for the life of the app and only
    // flips `open`, so nothing resets on its own: without this, the dialog
    // reopens holding the last connection's name, file, colour and
    // production flag — and its "Reachable" verdict, which would then be
    // asserting reachability about a file the user is in the middle of
    // replacing. A form that opens pre-filled with someone else's answers is
    // a nuisance; one that opens with a stale verdict is misleading.
    //
    // On CLOSE as well as open, and the generation bump with it: a verdict
    // left standing on a closed dialog is a verdict standing on whatever
    // opens next, and `testing`/`saving` left true would reopen the form
    // with its own buttons disabled by a request nobody is waiting for any
    // more.
    generation.current++;
    setName('');
    setValues({});
    setColor(DEFAULT_COLOR);
    setProduction(false);
    setTestStatus(null);
    setFormError(null);
    setInvalid([]);
    setTesting(false);
    setSaving(false);
    setDrivers([]);
    setDriverId('');
    setDriversError(null);
    if (open) {
      // A type-only narrowing, not a runtime check: `document.activeElement`
      // is always at least `document.body` in a mounted document, and
      // nothing in this app ever focuses a non-HTML element, so there is no
      // reachable "it wasn't an HTMLElement" case to branch on.
      openerRef.current = document.activeElement as HTMLElement | null;
      // Nothing else moves focus into a modal that just appeared: without
      // this, Tab from wherever the trigger button was would walk into the
      // sidebar behind the dialog before ever reaching the dialog's own
      // controls.
      nameInputRef.current?.focus();
      // Asked on every open rather than once for the life of the app: the
      // engine is a separate process that can be restarted under us, and an
      // answer from a sidecar that has since died would be a form built on a
      // driver list nothing can honour.
      //
      // Guarded by the same generation counter as the other two requests —
      // an abandoned list landing on the form that replaced it would repaint
      // the picker under the user's hands.
      const gen = generation.current;
      void (async () => {
        try {
          const list = await listDrivers();
          if (gen !== generation.current) return;
          setDrivers(list);
          setDriverId(list[0]?.id ?? '');
          if (list.length === 0) {
            // Not an error the engine reported, but the same dead end: an
            // empty picker with nothing said is indistinguishable from a
            // dialog that lost its buttons.
            setDriversError({ message: 'This engine has no drivers registered, so there is nothing to connect to.' });
          }
        } catch (err) {
          if (gen !== generation.current) return;
          // The dialog still renders. Refusing to draw at all is the
          // blank-screen failure; drawing an empty picker with no
          // explanation is the same failure, quieter.
          setDriversError(describeError(err));
        }
      })();
    } else {
      openerRef.current?.focus();
      openerRef.current = null;
    }
  }, [open]);

  if (!open) return null;

  /**
   * The picked driver, and what it says it cannot dial without. Both are
   * derived, never stored: a second copy of the selected driver's own
   * requirements is precisely the duplication this dialog just stopped
   * keeping. Undefined until drivers.list answers — and for good, if it
   * never does, which is what disables Connect below.
   */
  const selected = drivers.find((d) => d.id === driverId);
  const requiredFields = selected?.required_fields ?? [];

  // No not-found branch: `handleKeyDown` only ever fires from a keydown
  // already dispatched on the rendered dialog element, so `dialogRef` is
  // always attached by the time this runs — the same reasoning Sidebar's
  // roving tabIndex uses for `rows` never being empty in its own handler.
  function focusableElements(): HTMLElement[] {
    return Array.from(dialogRef.current!.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR));
  }

  function buildConnection(): NewConnection {
    // Each field goes out under the name the ENGINE gave it: required_fields
    // speaks ConnConfig's vocabulary, and store.Saved's JSON keys are the
    // same words, so there is no mapping table here to drift out of step
    // with either.
    const fields: Record<string, string> = {};
    for (const field of requiredFields) fields[field] = fieldValue(field);
    return {
      name: name.trim(),
      driver: driverId,
      color,
      read_only: production,
      ...fields,
    };
  }

  /**
   * The fields the selected driver cannot dial without that are still empty.
   *
   * The list is the ENGINE's, fetched above — not a copy of it kept here.
   * The copy this replaces could only ever disagree with Go: it hardcoded
   * SQLite's File, so a driver needing a host and a user would have saved a
   * connection that can never dial, which is the defect a user already
   * reported against this dialog once, for SQLite.
   *
   * Name is checked separately, by handleConnect: every driver needs a name
   * to save a record under, and dialing does not care what it is called.
   */
  function missingFields(): string[] {
    return requiredFields.filter((field) => !fieldValue(field));
  }

  /**
   * What has been typed into one engine-named field, trimmed. Shared by the
   * check and the payload so "empty" and "what gets sent" cannot drift: a
   * field that trimmed to nothing for the check but shipped its spaces
   * anyway would be a connection saved with a whitespace host.
   */
  function fieldValue(field: string): string {
    return (values[field] ?? '').trim();
  }

  /** Refuses the same way the engine does, naming what is missing. */
  function refuseMissing(missing: string[]) {
    setFormError({ message: `${missing.map(fieldLabel).join(', ')} is required` });
    setInvalid(missing);
  }

  function selectDriver(id: string) {
    setDriverId(id);
    // A verdict about a SQLite file is not a verdict about a MySQL host, and
    // a refusal naming the last driver's fields is not about this one's —
    // the same reasoning the reset-on-open effect above is built on, at a
    // boundary the user crosses without closing anything.
    setTestStatus(null);
    setFormError(null);
    setInvalid([]);
  }

  function toggleProduction() {
    setProduction((prev) => {
      const next = !prev;
      setColor(next ? DANGER_COLOR : DEFAULT_COLOR);
      return next;
    });
  }

  async function handleTestConnection() {
    // No guard against re-entry, and none against a missing driver either:
    // the only trigger is the button below, which is disabled both while a
    // test is in flight and while there is no driver to test — and a
    // disabled <button> never dispatches a click at all. The browser
    // enforces both invariants, so a check here would be unreachable dead
    // code.
    const missing = missingFields();
    if (missing.length > 0) {
      refuseMissing(missing);
      setTestStatus(null);
      return;
    }
    setFormError(null);
    setInvalid([]);
    setTesting(true);
    setTestStatus(null);
    const gen = generation.current;
    try {
      const result = await testConnection(buildConnection(), '');
      if (gen !== generation.current) return;
      if (result.ok) {
        setTestStatus({ kind: 'ok' });
      } else {
        // A reachability verdict, not a rejection: the engine answered, so
        // there is no `native` to disclose — but it does classify the
        // failure, and that Kind is what the hint branches on.
        setTestStatus({ kind: 'error', error: { message: result.error ?? 'Connection failed', kind: result.kind } });
      }
    } catch (err) {
      if (gen !== generation.current) return;
      setTestStatus({ kind: 'error', error: describeError(err) });
    } finally {
      // Guarded like the rest: the close already cleared this, and a newer
      // request may be in flight by now — clearing its flag would re-enable
      // a button that is legitimately disabled.
      if (gen === generation.current) setTesting(false);
    }
  }

  async function handleConnect() {
    // Unlike Test Connection, this guard is load-bearing: Cmd/Ctrl+Enter
    // reaches this function directly from a keydown handler, which bypasses
    // the Connect button's `disabled` attribute entirely.
    if (saving) return;
    // Same bypass, second invariant: with no driver there is nothing to
    // build a connection out of, and the Connect button's own `disabled`
    // never sees a keyboard chord. The driver list's failure is already on
    // screen, so this says nothing more.
    if (!selected) return;
    const trimmedName = name.trim();
    if (!trimmedName) {
      setFormError({ message: 'Name is required' });
      setInvalid(['name']);
      return;
    }
    const missing = missingFields();
    if (missing.length > 0) {
      refuseMissing(missing);
      return;
    }
    setFormError(null);
    setInvalid([]);
    setSaving(true);
    const gen = generation.current;
    try {
      const stored = await saveConnection(buildConnection(), '');
      // onSaved closes the dialog (App.tsx): firing it for an abandoned save
      // would shut the form the user just opened. The record is saved either
      // way — the residue is a sidebar list that does not show it until it
      // next refreshes, which is the better half of the trade.
      if (gen !== generation.current) return;
      onSaved(stored);
    } catch (err) {
      if (gen !== generation.current) return;
      setFormError(describeError(err));
    } finally {
      if (gen === generation.current) setSaving(false);
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
      return;
    }
    if (e.key === 'Tab') {
      const focusable = focusableElements();
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      // Only the two boundaries are trapped — every other Tab/Shift+Tab is
      // left to the browser's normal focus order within the dialog.
      if (e.shiftKey) {
        if (document.activeElement === first) {
          e.preventDefault();
          last.focus();
        }
      } else if (document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
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
        ref={dialogRef}
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
            {/*
              One button per driver the engine reported, in the order it
              reported them (driver.IDs sorts, so that order is stable). This
              used to be three literal buttons with two of them permanently
              disabled, which made registering a driver in Go a change to
              this file as well.
            */}
            <div className="segmented" role="group" aria-labelledby="driver-label">
              {drivers.map((d) => (
                <button
                  key={d.id}
                  type="button"
                  aria-pressed={d.id === driverId}
                  onClick={() => selectDriver(d.id)}
                >
                  {DRIVER_LABELS[d.id] ?? d.id}
                </button>
              ))}
            </div>
            {driversError && (
              <div role="alert" className="field-error">
                <ErrorText description={driversError} />
              </div>
            )}
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
              aria-invalid={invalid.includes('name') ? 'true' : undefined}
            />
          </div>

          {/*
            The form below the Name field is the engine's answer, not this
            file's: one input per field the picked driver named. Monospace
            for all of them — every one of these is a machine identifier (a
            path, a host, an account), not prose.
          */}
          {requiredFields.map((field) => (
            <div className="field" key={field}>
              <label className="field-label" htmlFor={`conn-${field}`}>
                {fieldLabel(field)}
                <span aria-hidden="true"> *</span>
              </label>
              <input
                id={`conn-${field}`}
                className="text-input mono"
                type="text"
                value={values[field] ?? ''}
                onChange={(e) => setValues((prev) => ({ ...prev, [field]: e.target.value }))}
                placeholder={FIELD_PLACEHOLDERS[field]}
                aria-required="true"
                aria-invalid={invalid.includes(field) ? 'true' : undefined}
              />
            </div>
          ))}

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
            <div role="alert" className="field-error">
              <ErrorText description={formError} />
            </div>
          )}
        </div>

        <div className="dialog-footer">
          <button
            type="button"
            className="btn"
            onClick={() => void handleTestConnection()}
            disabled={testing || !selected}
          >
            Test Connection
          </button>
          {testStatus?.kind === 'ok' && (
            <span className="test-result ok">
              <span className="result-dot" />
              Reachable
            </span>
          )}
          {testStatus?.kind === 'error' && (
            <div className="test-result error">
              <span className="result-dot" />
              <ErrorText description={testStatus.error} />
            </div>
          )}
          <span className="spacer" />
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button
            type="button"
            className="btn primary"
            onClick={() => void handleConnect()}
            disabled={saving || !selected}
          >
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
