import { useEffect, useState } from 'react'

// KeepScreenOn is Kitchen reading mode's Keep screen on switch: whether it is
// on, setOn, whether this browser can keep the screen on at all, and why the
// screen could not be kept on, if it could not.
export type KeepScreenOn = {
  on: boolean
  setOn: (on: boolean) => void
  supported: boolean
  failure: string | null
}

// useKeepScreenOn holds a Screen Wake Lock while the switch is on, and only
// then. It starts off on each visit. The browser lets the lock go whenever
// the page is hidden, so it is asked for again each time the page is visible
// while the switch is on. Turning the switch off or leaving the screen
// releases it, including a lock still being granted as the screen is left.
//
// A browser without the Screen Wake Lock API (or a page not served over
// HTTPS) cannot keep the screen on: supported is false and the switch must
// not be offered as working. A request the browser refuses turns the switch
// back off and is reported as failure; turning it on again asks again.
export function useKeepScreenOn(): KeepScreenOn {
  const [on, setOnState] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)
  const supported = 'wakeLock' in navigator

  useEffect(() => {
    if (!on) return
    const wakeLock = navigator.wakeLock
    let sentinel: WakeLockSentinel | null = null
    let requesting = false
    let left = false

    function acquire() {
      if (document.visibilityState !== 'visible' || requesting) return
      if (sentinel !== null && !sentinel.released) return
      requesting = true
      wakeLock.request('screen').then(
        (granted) => {
          requesting = false
          if (left) void granted.release()
          else sentinel = granted
        },
        (err: unknown) => {
          requesting = false
          if (left) return
          setFailure(String(err))
          setOnState(false)
        },
      )
    }

    acquire()
    document.addEventListener('visibilitychange', acquire)
    return () => {
      left = true
      document.removeEventListener('visibilitychange', acquire)
      if (sentinel !== null) void sentinel.release()
    }
  }, [on])

  function setOn(next: boolean) {
    if (next) setFailure(null)
    setOnState(next)
  }

  return { on, setOn, supported, failure }
}
