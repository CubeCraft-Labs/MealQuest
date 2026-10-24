import { useRef, useState } from 'react'

// Refusal is one change the server turned down, or one it made that left
// something wrong on this device. key is unique to that attempt, so two
// attempts of the same change in flight each keep their own refusal. change
// names what was tried, so trying it again once it has been refused clears
// the earlier refusal and no other.
export type Refusal = { key: number; change: string; text: string }

// useRefusals keeps each change's own refusal: the mutation hooks only
// report their latest change, which would hide a refusal while another
// change is in flight. track(change, what, answer, left) clears change's
// earlier refusal and records "Could not {what}: …" if answer rejects, or
// what left says the change the server made left wrong, if anything.
export function useRefusals() {
  const [refusals, setRefusals] = useState<Refusal[]>([])
  const attempts = useRef(0)

  function track<T>(change: string, what: string, answer: Promise<T>, left?: (made: T) => string | null) {
    const key = ++attempts.current
    const refuse = (text: string) => setRefusals((all) => [...all, { key, change, text }])
    setRefusals((all) => all.filter((refusal) => refusal.change !== change))
    answer.then(
      (made) => {
        const text = left?.(made) ?? null
        if (text !== null) refuse(text)
      },
      (error: Error) => refuse(`Could not ${what}: ${error.message}`),
    )
  }

  return { refusals, track }
}
