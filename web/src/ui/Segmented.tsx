import { useRef, type KeyboardEvent } from 'react'
import styles from './Segmented.module.css'

export type SegmentedOption<T extends string> = {
  value: T
  label: string
}

export type SegmentedProps<T extends string> = {
  // Accessible name of the radiogroup.
  label: string
  options: readonly SegmentedOption<T>[]
  value: T
  onValueChange: (value: T) => void
  className?: string
}

const step: Record<string, number> = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }

// Segmented control with radiogroup semantics: one tab stop (the selected
// option); arrow keys move the selection and focus, wrapping at the ends;
// Home and End jump to the first and last option.
export function Segmented<T extends string>({ label, options, value, onValueChange, className }: SegmentedProps<T>) {
  const refs = useRef<(HTMLButtonElement | null)[]>([])
  const selected = options.findIndex((option) => option.value === value)
  if (selected === -1) {
    throw new Error(`Segmented "${label}": value "${value}" is not one of its options`)
  }

  function select(index: number) {
    const option = options[index]
    if (!option) throw new Error(`Segmented "${label}": no option at index ${index}`)
    onValueChange(option.value)
    refs.current[index]?.focus()
  }

  function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const delta = step[event.key]
    let next: number
    if (delta !== undefined) next = (selected + delta + options.length) % options.length
    else if (event.key === 'Home') next = 0
    else if (event.key === 'End') next = options.length - 1
    else return
    event.preventDefault()
    select(next)
  }

  return (
    <div
      role="radiogroup"
      aria-label={label}
      className={[styles.track, className].filter(Boolean).join(' ')}
      onKeyDown={onKeyDown}
    >
      {options.map((option, index) => {
        const checked = index === selected
        return (
          <button
            key={option.value}
            ref={(el) => {
              refs.current[index] = el
            }}
            type="button"
            role="radio"
            aria-checked={checked}
            tabIndex={checked ? 0 : -1}
            className={checked ? `${styles.option} ${styles.selected}` : styles.option}
            onClick={() => select(index)}
          >
            {option.label}
          </button>
        )
      })}
    </div>
  )
}
