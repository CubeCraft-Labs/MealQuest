import type { ReactNode } from 'react'
import styles from './ListRow.module.css'

export type ListRowProps = {
  title: ReactNode
  meta?: ReactNode
  // Leading slot: icon, photo or avatar.
  leading?: ReactNode
  // Trailing slot: chevron, toggle, count or action.
  trailing?: ReactNode
  // When set, the whole row is a button.
  onClick?: () => void
  className?: string
}

export function ListRow({ title, meta, leading, trailing, onClick, className }: ListRowProps) {
  const content = (
    <>
      {leading && <span className={styles.leading}>{leading}</span>}
      <span className={styles.body}>
        <span className={styles.title}>{title}</span>
        {meta && <span className={styles.meta}>{meta}</span>}
      </span>
      {trailing && <span className={styles.trailing}>{trailing}</span>}
    </>
  )
  const classes = [styles.row, className].filter(Boolean).join(' ')
  if (onClick) {
    return (
      <button type="button" className={`${classes} ${styles.interactive}`} onClick={onClick}>
        {content}
      </button>
    )
  }
  return <div className={classes}>{content}</div>
}
