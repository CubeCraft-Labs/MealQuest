import { useState } from 'react'

// useKeepScreenOn is Kitchen reading mode's Keep screen on switch: whether it
// is on, and a setter. It starts off on each visit.
//
// TODO(c7da10dc): hold a Screen Wake Lock while it is on. Today it is only
// the switch's state; nothing keeps the screen awake yet.
export function useKeepScreenOn(): [boolean, (on: boolean) => void] {
  return useState(false)
}
