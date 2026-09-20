import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vitest/config'

// The unit tests cover pure logic only (no Vue, no Nuxt runtime), so plain
// Vitest in Node is enough; the aliases mirror Nuxt's.
export default defineConfig({
  resolve: {
    alias: {
      '~': fileURLToPath(new URL('./app', import.meta.url)),
      '@': fileURLToPath(new URL('./app', import.meta.url)),
    },
  },
  test: {
    environment: 'node',
    include: ['tests/**/*.test.ts'],
  },
})
