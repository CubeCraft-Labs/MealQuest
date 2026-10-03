import type { ReactNode } from 'react'
import { Button } from './Button.tsx'
import styles from './EmptyState.module.css'

export type EmptyStateAction = {
  label: string
  onClick: () => void
}

export type EmptyStateProps = {
  title: string
  children: ReactNode
  // Optional primary action, rendered as a primary Button.
  action?: EmptyStateAction
}

// Bagel title, body text and an optional primary action.
export function EmptyState({ title, children, action }: EmptyStateProps) {
  return (
    <section className={styles.empty}>
      <h2 className={styles.title}>{title}</h2>
      <p className={styles.body}>{children}</p>
      {action && (
        <Button className={styles.action} onClick={action.onClick}>
          {action.label}
        </Button>
      )}
    </section>
  )
}
