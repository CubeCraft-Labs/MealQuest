import { fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import { expectMinHitArea } from '../test/hitArea.ts'
import { Segmented } from './Segmented.tsx'
import styles from './Segmented.module.css'

const options = [
  { value: 'week', label: 'Week' },
  { value: 'month', label: 'Month' },
  { value: 'year', label: 'Year' },
] as const

type Period = (typeof options)[number]['value']

function Harness({ initial = 'week' }: { initial?: Period }) {
  const [value, setValue] = useState<Period>(initial)
  return <Segmented label="Archive period" options={options} value={value} onValueChange={setValue} />
}

function radio(name: string) {
  return screen.getByRole('radio', { name })
}

describe('Segmented', () => {
  it('is a named radiogroup of radios with one checked', () => {
    render(<Harness />)
    expect(screen.getByRole('radiogroup', { name: 'Archive period' })).toBeInTheDocument()
    expect(screen.getAllByRole('radio')).toHaveLength(3)
    expect(radio('Week')).toHaveAttribute('aria-checked', 'true')
    expect(radio('Month')).toHaveAttribute('aria-checked', 'false')
    expect(radio('Week')).toHaveClass(styles.selected!)
  })

  it('has a single tab stop on the selected option', () => {
    render(<Harness initial="month" />)
    expect(radio('Month')).toHaveAttribute('tabindex', '0')
    expect(radio('Week')).toHaveAttribute('tabindex', '-1')
    expect(radio('Year')).toHaveAttribute('tabindex', '-1')
  })

  it('moves selection and focus with the arrow keys, wrapping at the ends', () => {
    render(<Harness />)
    radio('Week').focus()

    fireEvent.keyDown(radio('Week'), { key: 'ArrowRight' })
    expect(radio('Month')).toHaveAttribute('aria-checked', 'true')
    expect(radio('Month')).toHaveFocus()

    fireEvent.keyDown(radio('Month'), { key: 'ArrowDown' })
    expect(radio('Year')).toHaveAttribute('aria-checked', 'true')

    fireEvent.keyDown(radio('Year'), { key: 'ArrowRight' })
    expect(radio('Week')).toHaveAttribute('aria-checked', 'true')
    expect(radio('Week')).toHaveFocus()

    fireEvent.keyDown(radio('Week'), { key: 'ArrowLeft' })
    expect(radio('Year')).toHaveAttribute('aria-checked', 'true')

    fireEvent.keyDown(radio('Year'), { key: 'ArrowUp' })
    expect(radio('Month')).toHaveAttribute('aria-checked', 'true')
    expect(radio('Month')).toHaveFocus()
    expect(radio('Year')).toHaveAttribute('aria-checked', 'false')
  })

  it('jumps to the first and last option with Home and End', () => {
    render(<Harness initial="month" />)
    fireEvent.keyDown(radio('Month'), { key: 'End' })
    expect(radio('Year')).toHaveAttribute('aria-checked', 'true')
    fireEvent.keyDown(radio('Year'), { key: 'Home' })
    expect(radio('Week')).toHaveAttribute('aria-checked', 'true')
  })

  it('selects an option on click', () => {
    render(<Harness />)
    fireEvent.click(radio('Year'))
    expect(radio('Year')).toHaveAttribute('aria-checked', 'true')
    expect(radio('Week')).toHaveAttribute('aria-checked', 'false')
  })

  it('gives every option a ≥ 44 px hit area', () => {
    render(<Harness />)
    for (const option of screen.getAllByRole('radio')) expectMinHitArea(option)
  })

  it('names an option by its ariaLabel when it has one', () => {
    const sizes = [
      { value: 'small', label: 'A', ariaLabel: 'Small text' },
      { value: 'large', label: 'A', ariaLabel: 'Large text' },
    ] as const
    render(<Segmented label="Text size" options={sizes} value="large" onValueChange={() => {}} />)
    expect(radio('Small text')).toHaveTextContent('A')
    expect(radio('Large text')).toHaveAttribute('aria-checked', 'true')
  })

  it('fails fast when the value is not one of the options', () => {
    expect(() =>
      render(<Segmented label="Archive period" options={options} value={'decade' as Period} onValueChange={() => {}} />),
    ).toThrow('value "decade" is not one of its options')
  })
})
