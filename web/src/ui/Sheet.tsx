import { useEffect, useRef, type KeyboardEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import styles from './Sheet.module.css'

// The dialog's accessible name: exactly one of a label or a labelling element.
type SheetName =
  | { 'aria-label': string; 'aria-labelledby'?: never }
  | { 'aria-labelledby': string; 'aria-label'?: never }

export type SheetProps = SheetName & {
  open: boolean
  onClose: () => void
  children: ReactNode
}

const focusableSelector = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

function focusables(panel: HTMLElement): HTMLElement[] {
  return Array.from(panel.querySelectorAll<HTMLElement>(focusableSelector))
}

// Modal bottom sheet. Portalled to <body> so everything else there can be made
// inert while it is open. Focus moves in on open, Tab is trapped inside, and
// focus returns to the opener on close. Escape and a scrim click call onClose.
export function Sheet({ open, onClose, children, ...name }: SheetProps) {
  const rootRef = useRef<HTMLDivElement>(null)
  const panelRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const root = rootRef.current
    const panel = panelRef.current
    if (!open || !root || !panel) return

    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const background = Array.from(document.body.children).filter(
      (element) => element !== root && !element.hasAttribute('inert'),
    )
    for (const element of background) element.setAttribute('inert', '')
    ;(focusables(panel)[0] ?? panel).focus()

    return () => {
      for (const element of background) element.removeAttribute('inert')
      opener?.focus()
    }
  }, [open])

  if (!open) return null

  function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === 'Escape') {
      event.stopPropagation()
      onClose()
      return
    }
    if (event.key !== 'Tab' || !panelRef.current) return
    const items = focusables(panelRef.current)
    const first = items[0]
    const last = items[items.length - 1]
    if (!first || !last) {
      event.preventDefault()
      return
    }
    const active = document.activeElement
    if (event.shiftKey && (active === first || active === panelRef.current)) {
      event.preventDefault()
      last.focus()
    } else if (!event.shiftKey && active === last) {
      event.preventDefault()
      first.focus()
    }
  }

  return createPortal(
    <div ref={rootRef} className={styles.root}>
      <div className={styles.scrim} aria-hidden="true" onClick={onClose} />
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        tabIndex={-1}
        className={styles.panel}
        onKeyDown={onKeyDown}
        {...name}
      >
        {children}
      </div>
    </div>,
    document.body,
  )
}
