import type { ComponentPropsWithRef } from 'react'
import styles from './Card.module.css'

export type CardTone = 'surface' | 'muted'

export type CardProps = ComponentPropsWithRef<'div'> & {
  // surface = --color-surface; muted = the secondary neutral-100 fill.
  tone?: CardTone
}

export function Card({ tone = 'surface', className, ...rest }: CardProps) {
  const classes = [styles.card, styles[tone], className].filter(Boolean).join(' ')
  return <div className={classes} {...rest} />
}
