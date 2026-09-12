import { Component, type ReactNode } from 'react';
import { describeError, type ErrorDescription } from '../lib/errors';
import { ErrorText } from './ErrorText';
import './ErrorBoundary.css';

/**
 * The floor under every render in the app.
 *
 * React unmounts the whole tree when a render throws, so without a boundary
 * every one of those failures wears the same face: the window goes blank,
 * with nothing to read and nothing to click. This shell has shipped that
 * twice — a nil slice marshalled to `null` where the type declared an array
 * (see `asTables` in Sidebar.tsx), and a catalog shape the UI trusted — and
 * on both occasions the type said it could not happen. So this is not a
 * substitute for validating a payload at the seam it arrives at; it is what
 * catches the payload nobody has thought to validate yet.
 *
 * A class, because `getDerivedStateFromError` is the one React API with no
 * hook equivalent: there is no function-component form of a boundary.
 *
 * No `componentDidCatch`: React already logs a caught error with its
 * component stack, and a second log of the same thing is noise. What this
 * adds is the half React cannot — something on screen.
 */
export class ErrorBoundary extends Component<{ children: ReactNode }, { error: ErrorDescription | null }> {
  state: { error: ErrorDescription | null } = { error: null };

  /**
   * Anything at all can be thrown, and in this app the likeliest thing is
   * not an `Error`: `request()` rejects with a plain object on every path it
   * has. describeError is what turns either into a sentence rather than
   * `[object Object]` — the same normalization every other failure surface
   * in the shell renders through.
   */
  static getDerivedStateFromError(error: unknown) {
    return { error: describeError(error) };
  }

  render() {
    const { error } = this.state;
    if (!error) return this.props.children;
    return (
      <div className="error-boundary" role="alert">
        <b className="error-boundary-title">Lantern stopped drawing this window.</b>
        <ErrorText description={error} />
        <p className="error-boundary-note">
          Reloading redraws the window. Saved connections live in the engine and are not affected.
        </p>
        <button
          type="button"
          className="error-boundary-reload"
          onClick={() => window.location.reload()}
        >
          Reload Lantern
        </button>
      </div>
    );
  }
}
