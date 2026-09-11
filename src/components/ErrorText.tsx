import type { DbErrorKind } from '../lib/connections';
import type { ErrorDescription } from '../lib/errors';
import './ErrorText.css';

/**
 * What to do next, for the Kinds that imply something specific.
 *
 * Spec §11 says the UI branches on `Kind`; this is the first branch that is
 * not `canceled`, so it sets the pattern deliberately. A map rather than a
 * chain of conditions, because later Kinds are entries rather than edits;
 * and a hint that sits *beside* the engine's message rather than replacing
 * it, because the engine is the authority on what happened and only the UI
 * knows which control fixes it. A Kind whose fix is not a single, nameable
 * action has no entry — most do not, and inventing generic advice for them
 * would make the specific ones worth less.
 *
 * `read_only` is the whole reason the Kind exists: the engine refuses the
 * write because this connection opens read-only, and one toggle in the
 * connection dialog is what changes that.
 */
const KIND_HINTS: Partial<Record<DbErrorKind, string>> = {
  read_only: 'Untick “Production connection” on this connection to allow writes.',
};

/**
 * The one way a failure is rendered, everywhere in the shell.
 *
 * Spec §11 splits an error in two: a `Message` in engine-neutral terms, and
 * the driver's own `Native` text "shown on demand". This is the on-demand
 * half — a collapsed disclosure, so the driver's wording is one keystroke
 * away when someone needs to search for it, and never in the way when they
 * do not.
 *
 * Deliberately NOT `role="alert"`: the containing element already carries
 * one wherever an alert is warranted, and nesting a second inside it makes a
 * screen reader announce the same failure twice.
 */
export function ErrorText({ description }: { description: ErrorDescription }) {
  const { message, native, kind } = description;
  const hint = kind && KIND_HINTS[kind];
  return (
    <div className="error-text">
      <span className="error-text-message">{message}</span>
      {hint && <span className="error-text-hint">{hint}</span>}
      {/*
        A native identical to the message is the SQLite-today case, and a
        disclosure that opens onto a copy of the line above it reads as
        broken rather than as detail. Only a native that actually differs
        earns the extra control.
      */}
      {native && native !== message && (
        <details className="error-text-native">
          <summary>Details</summary>
          <span className="error-text-native-body">{native}</span>
        </details>
      )}
    </div>
  );
}
