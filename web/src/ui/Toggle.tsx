import type { ComponentPropsWithRef, KeyboardEvent } from 'react'
import styles from './Toggle.module.css'

export type ToggleProps = Omit<
  ComponentPropsWithRef<'button'>,
  'type' | 'role' | 'aria-checked' | 'onChange' | 'onClick' | 'onKeyDown' | 'onKeyUp' | 'children'
> & {
  checked: boolean
  onCheckedChange: (checked: boolean) => void
}

// 40×24 pill switch (role="switch"). Name it with aria-label or
// aria-labelledby. Space and Enter are handled here and their native button
// activation is suppressed, so each key press toggles exactly once.
export function Toggle({ checked, onCheckedChange, className, ...rest }: ToggleProps) {
  function onKeyDown(event: KeyboardEvent<HTMLButtonElement>) {
    if (event.key !== ' ' && event.key !== 'Enter') return
    event.preventDefault()
    onCheckedChange(!checked)
  }

  function onKeyUp(event: KeyboardEvent<HTMLButtonElement>) {
    if (event.key === ' ') event.preventDefault()
  }

  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      className={[styles.toggle, className].filter(Boolean).join(' ')}
      onClick={() => onCheckedChange(!checked)}
      onKeyDown={onKeyDown}
      onKeyUp={onKeyUp}
      {...rest}
    >
      <span className={styles.track} aria-hidden="true">
        <span className={styles.knob} />
      </span>
    </button>
  )
}
