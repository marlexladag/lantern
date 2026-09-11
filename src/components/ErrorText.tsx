import type { ErrorDescription } from '../lib/errors';
import './ErrorText.css';

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
  const { message, native } = description;
  return (
    <div className="error-text">
      <span className="error-text-message">{message}</span>
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
