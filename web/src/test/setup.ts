import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach, beforeEach } from 'vitest'
import { resetArchived } from '../dinners/archived.ts'
import { installLocks } from './locks.ts'

// jsdom has no Web Locks; each test's tabs share a new device's (src/test/locks.ts).
beforeEach(() => {
  if (typeof window !== 'undefined') installLocks()
})

afterEach(() => {
  cleanup()
  // The verified-session marker and other device state must not leak between
  // tests. Tests in the node environment (src/pwa.test.ts) have no storage.
  if (typeof localStorage !== 'undefined') localStorage.clear()
  resetArchived()
})
