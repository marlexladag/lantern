import { it, expect, vi, afterEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { ErrorBoundary } from './ErrorBoundary';

/*
 * React unmounts the entire tree when a render throws, so a boundary is the
 * only thing between one bad payload and a white rectangle. This shell has
 * shipped that failure twice, and both times the TYPE said the value could
 * not be there — which is why this exists alongside the payload validation
 * rather than instead of it.
 *
 * React logs every error a boundary catches, by design. Silenced here so a
 * deliberately thrown fixture does not read as a failing run.
 */
const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
afterEach(() => consoleError.mockClear());

function Boom({ throws }: { throws: unknown }): never {
  throw throws;
}

it('renders its children while nothing throws', () => {
  render(
    <ErrorBoundary>
      <span>the app</span>
    </ErrorBoundary>,
  );
  expect(screen.getByText('the app')).toBeDefined();
});

it('catches a render-time throw from a child and says what happened', () => {
  render(
    <ErrorBoundary>
      <Boom throws={new TypeError('drivers.map is not a function')} />
    </ErrorBoundary>,
  );

  expect(screen.getByRole('alert')).toBeDefined();
  expect(screen.getByRole('alert').textContent).toMatch(/drivers\.map is not a function/);
});

/*
 * The shape that matters most here: `request()` rejects with an OBJECT on
 * every path it has, and a fallback rendering that with String(err) would
 * greet the user with `[object Object]` at the exact moment they have
 * nothing else to read. describeError is what keeps that honest.
 */
it('renders an engine error object as its message, never as [object Object]', () => {
  render(
    <ErrorBoundary>
      <Boom throws={{ code: -32002, message: 'the engine died' }} />
    </ErrorBoundary>,
  );

  expect(screen.getByRole('alert').textContent).toMatch(/the engine died/);
  expect(screen.getByRole('alert').textContent).not.toMatch(/object Object/);
});

// The affordance is the whole point: a blank window offers nothing to do,
// and this one offers the only action that can help.
it('reloads the window when the reload button is pressed', () => {
  const reload = vi.fn();
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { ...window.location, reload },
  });

  render(
    <ErrorBoundary>
      <Boom throws={new Error('boom')} />
    </ErrorBoundary>,
  );
  fireEvent.click(screen.getByRole('button', { name: /reload/i }));

  expect(reload).toHaveBeenCalled();
});
