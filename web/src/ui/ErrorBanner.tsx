import type { ReactNode } from 'react'
import { Button } from './Button.tsx'
import { Card } from './Card.tsx'
import styles from './ErrorBanner.module.css'

export type ErrorBannerProps = {
  // What went wrong, in the caller's words.
  children: ReactNode
  // What Retry does; without it there is nothing to retry and no button.
  onRetry?: () => void
  retryLabel?: string
}

// Muted neutral-100 card with grape-700 text and, given onRetry, a Retry
// button.
export function ErrorBanner({ children, onRetry, retryLabel = 'Retry' }: ErrorBannerProps) {
  return (
    <Card tone="muted" role="alert" className={styles.banner}>
      <p className={styles.message}>{children}</p>
      {onRetry !== undefined && <Button onClick={onRetry}>{retryLabel}</Button>}
    </Card>
  )
}
