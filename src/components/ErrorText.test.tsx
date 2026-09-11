import { it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { ErrorText } from './ErrorText';

it('renders the engine-neutral message', () => {
  render(<ErrorText description={{ message: 'the database reported an error' }} />);

  expect(screen.getByText('the database reported an error')).toBeDefined();
});

// A shell failure carries no driver text at all, which is the common case.
it('renders no disclosure when there is no native text', () => {
  render(<ErrorText description={{ message: 'the engine died' }} />);

  expect(screen.queryByText('Details')).toBeNull();
  expect(document.querySelector('details')).toBeNull();
});

// The SQLite-today case: the driver's text and the engine's message are the
// same string. A disclosure that opens onto a copy of the line above it
// reads as broken, so there must not be one.
it('renders no disclosure when native is identical to the message', () => {
  render(<ErrorText description={{ message: 'no such table: users', native: 'no such table: users' }} />);

  expect(screen.queryByText('Details')).toBeNull();
  expect(document.querySelector('details')).toBeNull();
});

it('discloses a distinct native text, collapsed by default', () => {
  render(
    <ErrorText
      description={{
        message: 'the database reported an error',
        native: 'UNIQUE constraint failed: users.email',
      }}
    />,
  );

  const details = document.querySelector('details') as HTMLDetailsElement;
  expect(details).not.toBeNull();
  // Collapsed: the native text is present in the DOM but not leading.
  expect(details.open).toBe(false);
  expect(screen.getByText('Details')).toBeDefined();
  expect(screen.getByText('UNIQUE constraint failed: users.email')).toBeDefined();
});

// Keyboard-first is a constraint, not a feature (spec §12): a disclosure a
// mouse can open and a keyboard cannot is not shipped. A native <summary> is
// in the tab order without a tabindex of our own.
it('exposes the disclosure as a native summary, which is keyboard-reachable', () => {
  render(<ErrorText description={{ message: 'boom', native: 'SQLITE_BUSY: database is locked' }} />);

  const summary = screen.getByText('Details');
  expect(summary.tagName).toBe('SUMMARY');
  expect(summary.parentElement?.tagName).toBe('DETAILS');
});

// The containing element already carries role="alert" where an alert is
// warranted (the sidebar's three failure surfaces, the dialog's form error).
// A second one nested inside it would announce twice.
it('is not itself an alert', () => {
  const { container } = render(<ErrorText description={{ message: 'boom', native: 'detail' }} />);

  expect(container.querySelector('[role="alert"]')).toBeNull();
});

/*
 * Spec §11 says the UI branches on Kind. This is the first branch that is not
 * `canceled`, and both halves of it live here on purpose: a hint that renders
 * for every Kind is not a branch, it is unconditional text that happens to be
 * right once.
 */
it('tells a read_only failure what to do about it, without replacing the engine message', () => {
  render(
    <ErrorText
      description={{
        kind: 'read_only',
        message: 'the connection is read-only',
        native: 'attempt to write a readonly database',
      }}
    />,
  );

  // The engine still says what happened...
  expect(screen.getByText('the connection is read-only')).toBeDefined();
  // ...and the UI says what to do next: one toggle, named exactly as the
  // dialog names it.
  expect(screen.getByText(/production connection/i)).toBeDefined();
});

it('offers no hint for a constraint failure, whose fix is the data, not the connection', () => {
  render(
    <ErrorText
      description={{
        kind: 'constraint',
        message: 'the database reported an error',
        native: 'UNIQUE constraint failed: users.email',
      }}
    />,
  );

  expect(screen.getByText('the database reported an error')).toBeDefined();
  expect(screen.queryByText(/production connection/i)).toBeNull();
});
