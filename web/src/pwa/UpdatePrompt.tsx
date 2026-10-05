import { useEffect, useRef, useState } from 'react'
import { X } from 'lucide-react'
import { Button } from '../ui/Button.tsx'
import { Card } from '../ui/Card.tsx'
import { Icon } from '../ui/Icon.tsx'
import { IconButton } from '../ui/IconButton.tsx'
import styles from './UpdatePrompt.module.css'

// UpdateContainer is the part of navigator.serviceWorker the prompt uses.
export type UpdateContainer = Pick<ServiceWorkerContainer, 'ready' | 'controller' | 'addEventListener' | 'removeEventListener'>

export type UpdatePromptProps = {
  // navigator.serviceWorker, or undefined where the browser has none.
  container: UpdateContainer | undefined
  // Reloads the page once the new worker controls it.
  reload: () => void
}

// SKIP_WAITING is the message src/sw.ts answers by activating the waiting
// worker.
export const SKIP_WAITING = { type: 'SKIP_WAITING' } as const

// UpdatePrompt offers to reload when a new service worker is installed and
// waiting to replace the one controlling the page. Reload asks the waiting
// worker to activate and reloads once it controls the page; dismissing hides
// the prompt until a different worker is waiting.
export function UpdatePrompt({ container, reload }: UpdatePromptProps) {
  const [waiting, setWaiting] = useState<ServiceWorker | null>(null)
  const [dismissed, setDismissed] = useState<ServiceWorker | null>(null)
  const [accepted, setAccepted] = useState<ServiceWorker | null>(null)
  const reloadListener = useRef<(() => void) | null>(null)

  useEffect(() => {
    if (container === undefined) return
    const sw = container
    let registration: ServiceWorkerRegistration | null = null
    let unmounted = false
    const watched = new Set<ServiceWorker>()

    // A waiting worker only replaces something when the page has a
    // controller; without one there is no controllerchange to reload on.
    function check() {
      if (registration === null) return
      setWaiting(sw.controller === null ? null : registration.waiting)
    }
    function watch(worker: ServiceWorker | null) {
      if (worker === null || watched.has(worker)) return
      watched.add(worker)
      worker.addEventListener('statechange', check)
    }
    function updateFound() {
      watch(registration?.installing ?? null)
    }

    void sw.ready.then((ready) => {
      if (unmounted) return
      registration = ready
      ready.addEventListener('updatefound', updateFound)
      watch(ready.installing)
      watch(ready.waiting)
      check()
    })
    return () => {
      unmounted = true
      registration?.removeEventListener('updatefound', updateFound)
      watched.forEach((worker) => worker.removeEventListener('statechange', check))
      if (reloadListener.current !== null) {
        sw.removeEventListener('controllerchange', reloadListener.current)
        reloadListener.current = null
      }
    }
  }, [container])

  if (container === undefined || waiting === null || waiting === dismissed || waiting === accepted) return null

  function update() {
    if (container === undefined || waiting === null || reloadListener.current !== null) return
    const listener = () => {
      reloadListener.current = null
      reload()
    }
    reloadListener.current = listener
    container.addEventListener('controllerchange', listener, { once: true })
    setAccepted(waiting)
    waiting.postMessage(SKIP_WAITING)
  }

  return (
    <Card role="status" className={styles.toast}>
      <p className={styles.message}>
        New version ready <span aria-hidden="true">·</span>
      </p>
      {/* The space keeps the copy "New version ready · Reload" in the text;
          a flex container does not render it. */}{' '}
      <Button onClick={update}>Reload</Button>
      <IconButton label="Dismiss" onClick={() => setDismissed(waiting)}>
        <Icon icon={X} />
      </IconButton>
    </Card>
  )
}
