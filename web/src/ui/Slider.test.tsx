import { fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import { expectMinHitArea } from '../test/hitArea.ts'
import { Slider } from './Slider.tsx'

function Harness() {
  const [value, setValue] = useState(0.7)
  return (
    <Slider
      aria-label="Creativity (temperature)"
      min={0}
      max={1}
      step={0.1}
      value={value}
      onChange={(event) => setValue(Number(event.target.value))}
    />
  )
}

describe('Slider', () => {
  it('is a native slider exposing its range and value', () => {
    render(<Harness />)
    const slider = screen.getByRole('slider', { name: 'Creativity (temperature)' })
    expect(slider).toHaveAttribute('type', 'range')
    expect(slider).toHaveAttribute('min', '0')
    expect(slider).toHaveAttribute('max', '1')
    expect(slider).toHaveValue('0.7')
  })

  it('updates the grape fill with the value', () => {
    render(<Harness />)
    const slider = screen.getByRole('slider')
    expect(slider.style.getPropertyValue('--slider-fill')).toBe('70%')
    fireEvent.change(slider, { target: { value: '0.2' } })
    expect(slider).toHaveValue('0.2')
    expect(slider.style.getPropertyValue('--slider-fill')).toBe('20%')
  })

  it('has a ≥ 44 px hit area', () => {
    render(<Harness />)
    expectMinHitArea(screen.getByRole('slider'))
  })

  it('fails fast on an empty range', () => {
    expect(() => render(<Slider aria-label="Bad" min={1} max={1} value={1} readOnly />)).toThrow(
      'max (1) must be greater than min (1)',
    )
  })

  it('accepts values at both ends of the range', () => {
    render(
      <>
        <Slider aria-label="At min" min={0} max={1} value={0} readOnly />
        <Slider aria-label="At max" min={0} max={1} value={1} readOnly />
      </>,
    )
    expect(screen.getByRole('slider', { name: 'At min' }).style.getPropertyValue('--slider-fill')).toBe('0%')
    expect(screen.getByRole('slider', { name: 'At max' }).style.getPropertyValue('--slider-fill')).toBe('100%')
  })

  it.each([
    [2, 'value (2) must be within [0, 1]'],
    [-0.1, 'value (-0.1) must be within [0, 1]'],
    [Number.NaN, 'value (NaN) must be within [0, 1]'],
  ])('fails fast when the value %s is outside the range', (value, message) => {
    expect(() => render(<Slider aria-label="Bad" min={0} max={1} value={value} readOnly />)).toThrow(message)
  })
})
