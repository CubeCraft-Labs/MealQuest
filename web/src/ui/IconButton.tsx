import type { ComponentPropsWithRef } from 'react'
import styles from './IconButton.module.css'

export type IconButtonSize = 40 | 52 | 62
export type IconButtonTone = 'surface' | 'neutral' | 'accent' | 'teal'

export type IconButtonProps = Omit<ComponentPropsWithRef<'button'>, 'aria-label'> & {
  // Accessible name; an icon alone never names a button.
  label: string
  size?: IconButtonSize
  tone?: IconButtonTone
  // Shows the label as an 11 px caption under the circle, which then names
  // the button.
  showLabel?: boolean
}

// Round icon button, named by its label and showing it only with showLabel.
// The 40 px size keeps a 44 px hit box (see the CSS); the 52 px size sits
// beside a 52 px pill button, as the recipe's heart; the 62 px size is the
// round action button with --shadow-md.
export function IconButton({
  label,
  size = 40,
  tone = 'surface',
  showLabel = false,
  type = 'button',
  className,
  children,
  ...rest
}: IconButtonProps) {
  const classes = [styles.button, styles[`size${size}`], showLabel && styles.labelled, className].filter(Boolean).join(' ')
  return (
    <button type={type} aria-label={showLabel ? undefined : label} className={classes} {...rest}>
      <span className={[styles.face, styles[tone]].join(' ')} aria-hidden="true">
        {children}
      </span>
      {showLabel && label}
    </button>
  )
}
