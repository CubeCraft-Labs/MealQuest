import type { CSSProperties, ComponentPropsWithRef } from 'react'
import styles from './Slider.module.css'

export type SliderProps = Omit<ComponentPropsWithRef<'input'>, 'type' | 'value' | 'min' | 'max'> & {
  value: number
  min: number
  max: number
}

// Native range input, styled: 8 px track, grape fill up to the value, 24 px
// knob with a 3 px grape ring. Controlled, because the fill follows `value`.
// Name it with aria-label or aria-labelledby.
export function Slider({ value, min, max, className, style, ...rest }: SliderProps) {
  if (!(max > min)) throw new Error(`Slider: max (${max}) must be greater than min (${min})`)
  const fill = `${((value - min) / (max - min)) * 100}%`
  return (
    <input
      type="range"
      value={value}
      min={min}
      max={max}
      className={[styles.slider, className].filter(Boolean).join(' ')}
      style={{ ...style, '--slider-fill': fill } as CSSProperties}
      {...rest}
    />
  )
}
