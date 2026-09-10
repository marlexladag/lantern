import { it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, act } from '@testing-library/react';

vi.mock('../lib/engine', () => ({
  health: vi.fn(),
  onStateChange: vi.fn(),
}));

import { health, onStateChange } from '../lib/engine';
import { EngineStatus } from './EngineStatus';

const healthMock = vi.mocked(health);
const onStateChangeMock = vi.mocked(onStateChange);

beforeEach(() => {
  healthMock.mockReset();
  onStateChangeMock.mockReset();
  onStateChangeMock.mockResolvedValue(() => {});
});

it('shows a connecting state before the handshake completes', () => {
  healthMock.mockReturnValue(new Promise(() => {}));

  render(<EngineStatus />);

  expect(screen.getByText(/connecting/i)).toBeDefined();
});

it('shows the engine version and pid after a successful handshake', async () => {
  healthMock.mockResolvedValue({
    status: 'ok',
    version: '1.2.3',
    commit: 'abc123',
    pid: 4242,
  });

  render(<EngineStatus />);

  await waitFor(() => {
    expect(screen.getByText(/1\.2\.3/)).toBeDefined();
    expect(screen.getByText(/4242/)).toBeDefined();
  });
});

it('shows the error message when the handshake fails', async () => {
  healthMock.mockRejectedValue('engine is not running');

  render(<EngineStatus />);

  await waitFor(() => {
    expect(screen.getByText(/engine is not running/i)).toBeDefined();
  });
});

it('reports a restart and re-runs the handshake when the engine recovers', async () => {
  healthMock.mockResolvedValue({
    status: 'ok',
    version: '1.2.3',
    commit: 'abc123',
    pid: 4242,
  });

  let emit: ((s: 'ready' | 'restarting' | 'down') => void) | undefined;
  onStateChangeMock.mockImplementation(async (cb) => {
    emit = cb;
    return () => {};
  });

  render(<EngineStatus />);
  await waitFor(() => expect(screen.getByText(/1\.2\.3/)).toBeDefined());

  await act(async () => {
    emit?.('restarting');
  });
  expect(screen.getByText(/restarting/i)).toBeDefined();

  healthMock.mockResolvedValue({
    status: 'ok',
    version: '1.2.3',
    commit: 'abc123',
    pid: 5555,
  });
  await act(async () => {
    emit?.('ready');
  });

  await waitFor(() => expect(screen.getByText(/5555/)).toBeDefined());
});
