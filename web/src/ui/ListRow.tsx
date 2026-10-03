import type { ReactNode } from 'react'
import styles from './ListRow.module.css'

export type ListRowProps = {
  title: ReactNode
  meta?: ReactNode
  // Leading slot: icon, photo or avatar.
  leading?: ReactNode
  // Trailing slot: chevron, toggle, count or action.
  trailing?: ReactNode
  // When set, the leading slot, title and meta form one button. The trailing
  // slot stays a sibling outside it, so a trailing toggle or action is never
  // nested in the row's button and never triggers the row's action.
  onClick?: () => void
  className?: string
}

export function ListRow({ title, meta, leading, trailing, onClick, className }: ListRowProps) {
  const main = (
    <>
      {leading && <span className={styles.leading}>{leading}</span>}
      <span className={styles.body}>
        <span className={styles.title}>{title}</span>
        {meta && <span className={styles.meta}>{meta}</span>}
      </span>
    </>
  )
  const trailingSlot = trailing && <span className={styles.trailing}>{trailing}</span>
  if (onClick) {
    return (
      <div className={[styles.row, styles.interactive, className].filter(Boolean).join(' ')}>
        <button type="button" className={styles.main} onClick={onClick}>
          {main}
        </button>
        {trailingSlot}
      </div>
    )
  }
  return (
    <div className={[styles.row, className].filter(Boolean).join(' ')}>
      {main}
      {trailingSlot}
    </div>
  )
}
