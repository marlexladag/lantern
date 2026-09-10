import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    environment: 'jsdom',
    globals: true,
    include: ['src/**/*.test.{ts,tsx}'],
    coverage: {
      provider: 'v8',
      // Measure EVERY source file, not just the ones a test happens to
      // import. Without an explicit include, v8 defaults to "only files
      // covered by tests", which quietly leaves untested files out of the
      // number entirely — a 100% that excludes whatever nobody tested.
      include: ['src/**/*.{ts,tsx}'],
      exclude: [
        'src/**/*.test.{ts,tsx}',
        // The bootstrap: one DOM query and a ReactDOM.createRoot call. A unit
        // test here would assert that React renders, not that our code is
        // correct. It is exercised by actually launching the app.
        'src/main.tsx',
      ],
      thresholds: {
        statements: 100,
        branches: 100,
        functions: 100,
        lines: 100,
      },
    },
  },
});
