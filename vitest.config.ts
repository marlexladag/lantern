import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    environment: 'jsdom',
    globals: true,
    include: ['src/**/*.test.{ts,tsx}'],
    coverage: {
      provider: 'v8',
      // No `include` override: coverage.include defaults to "only files
      // covered by tests", so this gate applies to the same scope `npm run
      // test:coverage` already reports rather than a wider one no test
      // suite has been written against. Two files fall outside that scope
      // today — src/App.tsx and src/main.tsx, the composition root and
      // bootstrap — which have no tests and no branches; see the coverage
      // report for the reasoning. Widening this to `coverage.all` (via an
      // explicit `include`) would pull those in and fail the build until
      // they are tested too.
      thresholds: {
        statements: 100,
        branches: 100,
        functions: 100,
        lines: 100,
      },
    },
  },
});
