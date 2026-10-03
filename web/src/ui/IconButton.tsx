import type { ComponentPropsWithRef } from 'react'
import styles from './IconButton.module.css'

export type IconButtonSize = 40 | 62
export type IconButtonTone = 'surface' | 'accent' | 'teal'

export type IconButtonProps = Omit<ComponentPropsWithRef<'button'>, 'aria-label'> & {
  // Accessible name; an icon alone never names a button.
  label: string
  size?: IconButtonSize
  tone?: IconButtonTone
}

// Round icon-only button. The 40 px size keeps a 44 px hit box (see the CSS);
// the 62 px size is the round action button with --shadow-md.
export function IconButton({
  label,
  size = 40,
  tone = 'surface',
  type = 'button',
  className,
  children,
  ...rest
}: IconButtonProps) {
  const classes = [styles.button, styles[`size${size}`], className].filter(Boolean).join(' ')
  return (
    <button type={type} aria-label={label} className={classes} {...rest}>
      <span className={[styles.face, styles[tone]].join(' ')} aria-hidden="true">
        {children}
      </span>
    </button>
  )
}
