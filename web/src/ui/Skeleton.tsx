import type { ComponentPropsWithRef } from 'react'
import styles from './Skeleton.module.css'

export type SkeletonProps = Omit<ComponentPropsWithRef<'div'>, 'children'>

// Loading placeholder: a pill in --color-surface with a 1.2 s opacity pulse.
// Full width and 16 px tall by default; size it with className or style.
// Hidden from assistive technology — announce loading on the container.
export function Skeleton({ className, ...rest }: SkeletonProps) {
  const classes = [styles.skeleton, className].filter(Boolean).join(' ')
  return <div aria-hidden="true" className={classes} {...rest} />
}
