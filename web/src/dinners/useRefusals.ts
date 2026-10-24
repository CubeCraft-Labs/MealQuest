import { useRef, useState } from 'react'

// Refusal is one change the server turned down. key is unique to that
// attempt, so two attempts of the same change in flight each keep their own
// refusal. change names what was tried, so trying it again once it has been
// refused clears the earlier refusal and no other.
export type Refusal = { key: number; change: string; text: string }

// useRefusals keeps each change's own refusal: the mutation hooks only
// report their latest change, which would hide a refusal while another
// change is in flight. track(change, what, answer) clears change's earlier
// refusal and records "Could not {what}: …" if answer rejects.
export function useRefusals() {
  const [refusals, setRefusals] = useState<Refusal[]>([])
  const attempts = useRef(0)

  function track(change: string, what: string, answer: Promise<unknown>) {
    const key = ++attempts.current
    setRefusals((all) => all.filter((refusal) => refusal.change !== change))
    answer.catch((error: Error) => {
      setRefusals((all) => [...all, { key, change, text: `Could not ${what}: ${error.message}` }])
    })
  }

  return { refusals, track }
}
