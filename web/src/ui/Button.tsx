import type { ComponentPropsWithRef } from 'react'
import styles from './Button.module.css'

export type ButtonVariant = 'primary' | 'outline' | 'dark'

export type ButtonProps = ComponentPropsWithRef<'button'> & {
  variant?: ButtonVariant
}

// Pill button. Primary is grape with --on-accent text; outline and dark are the
// secondary treatments from the handoff. `type` defaults to "button" so a
// Button inside a form never submits by accident.
export function Button({ variant = 'primary', type = 'button', className, ...rest }: ButtonProps) {
  const classes = [styles.button, styles[variant], className].filter(Boolean).join(' ')
  return <button type={type} className={classes} {...rest} />
}
