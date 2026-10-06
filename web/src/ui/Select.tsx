import { ChevronDown } from 'lucide-react'
import { useId, type ComponentPropsWithRef } from 'react'
import { Icon } from './Icon.tsx'
import styles from './Select.module.css'

export type SelectProps = Omit<ComponentPropsWithRef<'select'>, 'id' | 'multiple'> & {
  label: string
}

// Pill dropdown: a native select on the surface fill with a chevron, always
// associated with its label. Pass the options as children.
export function Select({ label, className, children, ...rest }: SelectProps) {
  const id = useId()
  return (
    <div className={styles.field}>
      <label htmlFor={id} className={styles.label}>
        {label}
      </label>
      <span className={styles.control}>
        <select id={id} className={[styles.select, className].filter(Boolean).join(' ')} {...rest}>
          {children}
        </select>
        <Icon icon={ChevronDown} className={styles.chevron} />
      </span>
    </div>
  )
}
