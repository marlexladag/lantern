import { it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';

// Stubbed so this test is about the composition root, not about EngineStatus —
// which has its own suite and would otherwise drag the engine client in.
vi.mock('./components/EngineStatus', () => ({
  EngineStatus: () => <div data-testid="engine-status" />,
}));

import App from './App';

it('renders the product name and mounts the engine status', () => {
  render(<App />);
  expect(screen.getByRole('heading', { name: 'Lantern' })).toBeDefined();
  expect(screen.getByTestId('engine-status')).toBeDefined();
});
