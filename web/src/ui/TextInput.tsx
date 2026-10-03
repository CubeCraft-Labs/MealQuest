import { useId, type ComponentPropsWithRef } from 'react'
import styles from './TextInput.module.css'

export type TextInputProps = Omit<ComponentPropsWithRef<'input'>, 'id'> & {
  label: string
  // Hide the label visually; it stays the input's accessible name.
  labelHidden?: boolean
}

// 48 px pill input on the surface fill, always associated with its label.
export function TextInput({ label, labelHidden = false, type = 'text', className, ...rest }: TextInputProps) {
  const id = useId()
  return (
    <div className={styles.field}>
      <label htmlFor={id} className={labelHidden ? styles.hiddenLabel : styles.label}>
        {label}
      </label>
      <input id={id} type={type} className={[styles.input, className].filter(Boolean).join(' ')} {...rest} />
    </div>
  )
}
