import type { ComponentPropsWithRef } from 'react'
import styles from './Tag.module.css'

export type TagTone = 'grape' | 'teal' | 'neutral'

export type TagProps = ComponentPropsWithRef<'span'> & {
  tone?: TagTone
}

// Static label such as "Draft", "Side quest", "Synced" or "Regular". Not
// interactive.
export function Tag({ tone = 'grape', className, ...rest }: TagProps) {
  const classes = [styles.tag, styles[tone], className].filter(Boolean).join(' ')
  return <span className={classes} {...rest} />
}
