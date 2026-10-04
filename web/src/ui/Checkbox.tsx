import type { ComponentPropsWithRef, ReactNode } from 'react'
import styles from './Checkbox.module.css'

export type CheckboxProps = Omit<ComponentPropsWithRef<'input'>, 'type'> & {
  label: ReactNode
}

// Native checkbox, styled. The wrapping label is the hit area (at least
// 44×44), so tapping the text toggles it too.
export function Checkbox({ label, className, ...rest }: CheckboxProps) {
  return (
    <label className={[styles.root, className].filter(Boolean).join(' ')}>
      <input type="checkbox" className={styles.input} {...rest} />
      <span className={styles.label}>{label}</span>
    </label>
  )
}
