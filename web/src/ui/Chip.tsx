import type { ComponentPropsWithRef } from 'react'
import styles from './Chip.module.css'

export type ChipProps = Omit<ComponentPropsWithRef<'span'>, 'children'> & {
  label: string
  // When set, the chip shows a "Remove {label}" button that calls it.
  onRemove?: () => void
}

export function Chip({ label, onRemove, className, ...rest }: ChipProps) {
  const classes = [styles.chip, onRemove && styles.removable, className].filter(Boolean).join(' ')
  return (
    <span className={classes} {...rest}>
      {label}
      {onRemove && (
        <button type="button" className={styles.remove} aria-label={`Remove ${label}`} onClick={onRemove}>
          <svg viewBox="0 0 12 12" width="10" height="10" aria-hidden="true">
            <path d="M2 2l8 8M10 2l-8 8" stroke="currentColor" strokeWidth="2.75" strokeLinecap="round" />
          </svg>
        </button>
      )}
    </span>
  )
}
