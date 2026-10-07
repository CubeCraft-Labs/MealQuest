import { useEffect, useId, type ReactNode } from 'react'
import { useBlocker } from 'react-router'
import { Button } from '../ui/Button.tsx'
import { Sheet } from '../ui/Sheet.tsx'
import styles from './LeaveGuard.module.css'

// LeaveGuard asks before leaving the screen with unsaved changes.
export function LeaveGuard({ dirty }: { dirty: boolean }) {
  const blocker = useBlocker(
    ({ currentLocation, nextLocation }) => dirty && currentLocation.pathname !== nextLocation.pathname,
  )
  useEffect(() => {
    if (!dirty) return
    const onBeforeUnload = (event: BeforeUnloadEvent) => event.preventDefault()
    window.addEventListener('beforeunload', onBeforeUnload)
    return () => window.removeEventListener('beforeunload', onBeforeUnload)
  }, [dirty])
  return (
    <Confirm
      open={blocker.state === 'blocked'}
      title="Leave without saving?"
      confirmLabel="Leave"
      cancelLabel="Keep editing"
      onCancel={() => blocker.reset?.()}
      onConfirm={() => blocker.proceed?.()}
    >
      Your changes on this screen have not been saved.
    </Confirm>
  )
}

type ConfirmProps = {
  open: boolean
  title: string
  children: ReactNode
  confirmLabel: string
  cancelLabel?: string
  busy?: boolean
  // When false, the cancel button, Escape and the scrim do nothing, so the
  // sheet stays until the action settles.
  dismissible?: boolean
  error?: string | null
  onCancel: () => void
  onConfirm: () => void
}

// Confirm is a sheet asking the household to confirm one action.
export function Confirm({
  open,
  title,
  children,
  confirmLabel,
  cancelLabel = 'Cancel',
  busy = false,
  dismissible = true,
  error = null,
  onCancel,
  onConfirm,
}: ConfirmProps) {
  const titleId = useId()
  function dismiss() {
    if (dismissible) onCancel()
  }
  return (
    <Sheet open={open} onClose={dismiss} aria-labelledby={titleId}>
      <div className={styles.sheet}>
        <h2 id={titleId} className={styles.title}>
          {title}
        </h2>
        <p className={styles.text}>{children}</p>
        {error !== null && (
          <p className={styles.error} role="alert">
            {error}
          </p>
        )}
        <div className={styles.actions}>
          <Button variant="outline" disabled={!dismissible} onClick={onCancel}>
            {cancelLabel}
          </Button>
          <Button disabled={busy} onClick={onConfirm}>
            {confirmLabel}
          </Button>
        </div>
      </div>
    </Sheet>
  )
}
