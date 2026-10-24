import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'
import { resetArchived } from '../dinners/archived.ts'

afterEach(() => {
  cleanup()
  // The verified-session marker and other device state must not leak between
  // tests. Tests in the node environment (src/pwa.test.ts) have no storage.
  if (typeof localStorage !== 'undefined') localStorage.clear()
  resetArchived()
})
