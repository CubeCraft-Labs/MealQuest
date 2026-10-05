// VERIFIED_SESSION_KEY is the localStorage key marking that GET /api/v1/me
// last answered with a household member on this device. It holds only "1" —
// never the /me response, its CSRF token or anything from /auth.
export const VERIFIED_SESSION_KEY = 'mealquest.verified-session'

export type VerifiedSession = {
  // verified reports whether /me last answered with a member on this device.
  verified(): boolean
  // remember records a /me that answered with a member.
  remember(): void
  // forget records that the server refused the session (401) or the account
  // (not in this household).
  forget(): void
}

// createVerifiedSession keeps the marker in storage (window.localStorage in
// the app). A storage failure is thrown to the caller.
export function createVerifiedSession(storage: Storage): VerifiedSession {
  return {
    verified: () => storage.getItem(VERIFIED_SESSION_KEY) === '1',
    remember: () => storage.setItem(VERIFIED_SESSION_KEY, '1'),
    forget: () => storage.removeItem(VERIFIED_SESSION_KEY),
  }
}
