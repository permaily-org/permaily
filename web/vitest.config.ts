import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

// Dates are written by Intl in the person's own time zone: the tests have one, so a day reads the same on
// every machine.
process.env.TZ = 'UTC'

// The tests have their own configuration and live in tests/, outside src/: nothing of them is part of
// `tsc -b` or `vite build`, so the built app is the same with or without them (docs/web.md).
export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    include: ['tests/**/*.test.{ts,tsx}'],
    setupFiles: ['tests/setup.ts'],
    coverage: {
      provider: 'v8',
      reporter: ['text'],
      // What talks to the server. The screens on sample data are left out: they are still changing.
      include: [
        'src/lib/**',
        'src/components/SessionGate.tsx',
        'src/components/Confirm.tsx',
        'src/components/brands/**',
        'src/pages/auth/**',
        'src/pages/settings/signin/**',
        'src/pages/settings/account/**',
        'src/pages/admin/**',
        'src/pages/accounts/**',
        'src/pages/inbox/**',
      ],
      // The measured values, rounded down. They only go up (docs/testing.md).
      thresholds: {
        'src/lib/**': { statements: 98, branches: 97, functions: 100, lines: 100 },
        'src/components/SessionGate.tsx': {
          statements: 98,
          branches: 97,
          functions: 100,
          lines: 100,
        },
        'src/components/Confirm.tsx': { statements: 96, branches: 91, functions: 94, lines: 100 },
        'src/components/brands/**': { statements: 100, branches: 100, functions: 100, lines: 100 },
        'src/pages/auth/**': { statements: 97, branches: 95, functions: 98, lines: 100 },
        'src/pages/settings/signin/**': { statements: 95, branches: 91, functions: 98, lines: 99 },
        'src/pages/settings/account/**': {
          statements: 96,
          branches: 90,
          functions: 100,
          lines: 99,
        },
        'src/pages/admin/**': { statements: 97, branches: 91, functions: 100, lines: 100 },
        'src/pages/accounts/**': { statements: 97, branches: 88, functions: 100, lines: 100 },
        'src/pages/inbox/**': { statements: 98, branches: 92, functions: 100, lines: 100 },
      },
    },
  },
})
