import { fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import '../styles/tokens.css'
import { animationKeyframes, declaredStyle, reducedMotionStyle } from '../test/cssRules.ts'
import { Sheet } from './Sheet.tsx'
import styles from './Sheet.module.css'

function Harness({ withControls = true }: { withControls?: boolean }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        Open Supply run
      </button>
      <Sheet aria-label="Supply run" open={open} onClose={() => setOpen(false)}>
        <p>Grouped by dinner</p>
        {withControls && (
          <>
            <button type="button">Try it</button>
            <button type="button">Pass</button>
          </>
        )}
      </Sheet>
    </>
  )
}

function openSheet() {
  const opener = screen.getByRole('button', { name: 'Open Supply run' })
  opener.focus()
  fireEvent.click(opener)
  return { opener, dialog: screen.getByRole('dialog', { name: 'Supply run' }) }
}

function scrim() {
  const element = document.querySelector(`.${styles.scrim}`)
  if (!element) throw new Error('scrim not rendered')
  return element
}

describe('Sheet', () => {
  it('renders nothing while closed', () => {
    render(<Harness />)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('is a labelled modal dialog', () => {
    render(<Harness />)
    const { dialog } = openSheet()
    expect(dialog).toHaveAttribute('aria-modal', 'true')
  })

  it('accepts aria-labelledby as its accessible name', () => {
    render(
      <>
        <h2 id="sheet-title">Quest log</h2>
        <Sheet aria-labelledby="sheet-title" open onClose={() => {}}>
          <p>Nothing yet</p>
        </Sheet>
      </>,
    )
    expect(screen.getByRole('dialog', { name: 'Quest log' })).toBeInTheDocument()
  })

  it('moves focus to the first control and restores it to the opener on close', () => {
    render(<Harness />)
    const { opener } = openSheet()
    expect(screen.getByRole('button', { name: 'Try it' })).toHaveFocus()
    fireEvent.keyDown(document.activeElement!, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(opener).toHaveFocus()
  })

  it('focuses the dialog itself when it has no focusable control', () => {
    render(<Harness withControls={false} />)
    const { dialog } = openSheet()
    expect(dialog).toHaveFocus()
  })

  it('skips controls that cannot take keyboard focus when moving focus in', () => {
    render(
      <Sheet aria-label="Quest log" open onClose={() => {}}>
        <button type="button" hidden>
          Hidden
        </button>
        <button type="button" style={{ display: 'none' }}>
          Not displayed
        </button>
        <button type="button" style={{ visibility: 'hidden' }}>
          Invisible
        </button>
        <button type="button" tabIndex={-1}>
          Not tabbable
        </button>
        <div aria-hidden="true">
          <button type="button">Aria hidden</button>
        </div>
        <button type="button">Try it</button>
      </Sheet>,
    )
    expect(screen.getByRole('button', { name: 'Try it' })).toHaveFocus()
  })

  it('focuses the dialog itself when every control is hidden', () => {
    render(
      <Sheet aria-label="Quest log" open onClose={() => {}}>
        <button type="button" hidden>
          Hidden
        </button>
      </Sheet>,
    )
    expect(screen.getByRole('dialog', { name: 'Quest log' })).toHaveFocus()
  })

  it('does not stop the Tab trap on a hidden control', () => {
    render(
      <Sheet aria-label="Quest log" open onClose={() => {}}>
        <button type="button" hidden>
          Hidden first
        </button>
        <button type="button">Try it</button>
        <button type="button">Pass</button>
        <button type="button" hidden>
          Hidden last
        </button>
      </Sheet>,
    )
    const first = screen.getByRole('button', { name: 'Try it' })
    const last = screen.getByRole('button', { name: 'Pass' })

    last.focus()
    fireEvent.keyDown(last, { key: 'Tab' })
    expect(first).toHaveFocus()

    fireEvent.keyDown(first, { key: 'Tab', shiftKey: true })
    expect(last).toHaveFocus()
  })

  it('wraps Tab and Shift+Tab inside the sheet', () => {
    render(<Harness />)
    openSheet()
    const first = screen.getByRole('button', { name: 'Try it' })
    const last = screen.getByRole('button', { name: 'Pass' })

    last.focus()
    fireEvent.keyDown(last, { key: 'Tab' })
    expect(first).toHaveFocus()

    fireEvent.keyDown(first, { key: 'Tab', shiftKey: true })
    expect(last).toHaveFocus()
  })

  it('keeps focus on the dialog when Tab has nowhere to go', () => {
    render(<Harness withControls={false} />)
    const { dialog } = openSheet()
    const tab = fireEvent.keyDown(dialog, { key: 'Tab' })
    expect(tab).toBe(false) // default prevented
    expect(dialog).toHaveFocus()
  })

  it('closes on Escape', () => {
    const onClose = vi.fn()
    render(
      <Sheet aria-label="Supply run" open onClose={onClose}>
        <button type="button">Try it</button>
      </Sheet>,
    )
    fireEvent.keyDown(screen.getByRole('button', { name: 'Try it' }), { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('closes on a scrim click but not on a panel click', () => {
    const onClose = vi.fn()
    render(
      <Sheet aria-label="Supply run" open onClose={onClose}>
        <p>Grouped by dinner</p>
      </Sheet>,
    )
    fireEvent.click(screen.getByRole('dialog'))
    fireEvent.click(screen.getByText('Grouped by dinner'))
    expect(onClose).not.toHaveBeenCalled()
    fireEvent.click(scrim())
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('makes the background inert while open and restores it on close', () => {
    const { container } = render(<Harness />)
    openSheet()
    const root = screen.getByRole('dialog').parentElement!
    expect(root.parentElement).toBe(document.body)
    expect(container).toHaveAttribute('inert')
    expect(root).not.toHaveAttribute('inert')

    fireEvent.click(scrim())
    expect(container).not.toHaveAttribute('inert')
  })

  it('leaves an already-inert background element inert after close', () => {
    const other = document.createElement('div')
    other.setAttribute('inert', '')
    document.body.append(other)
    try {
      render(<Harness />)
      openSheet()
      fireEvent.click(scrim())
      expect(other).toHaveAttribute('inert')
    } finally {
      other.remove()
    }
  })

  it('rises from 100% to 0 over 320 ms with the soft ease-out', () => {
    render(<Harness />)
    const { dialog } = openSheet()
    expect(declaredStyle(dialog).animation.split(' ').slice(1)).toEqual(['320ms', 'var(--ease-out-soft)'])
    const easing = getComputedStyle(document.documentElement).getPropertyValue('--ease-out-soft')
    expect(easing.replace(/\s/g, '')).toBe('cubic-bezier(.2,.8,.2,1)')
    const rise = animationKeyframes(dialog)
    expect(rise.findRule('from')?.style.transform).toBe('translateY(100%)')
    expect(rise.findRule('to')?.style.transform).toBe('translateY(0)')
  })

  it('fades the scrim in to 50% neutral-900', () => {
    render(<Harness />)
    openSheet()
    const style = declaredStyle(scrim())
    expect(style.background).toBe('color-mix(in srgb, var(--color-neutral-900) 50%, transparent)')
    expect(style.animation.split(' ').slice(1)).toEqual(['320ms', 'var(--ease-out-soft)'])
    const fade = animationKeyframes(scrim())
    expect(fade.findRule('from')?.style.opacity).toBe('0')
    expect(fade.findRule('to')?.style.opacity).toBe('1')
  })

  it('opens in place with no rise or fade under reduced motion', () => {
    render(<Harness />)
    const { dialog } = openSheet()
    expect(reducedMotionStyle(dialog).animation).toBe('none')
    expect(reducedMotionStyle(scrim()).animation).toBe('none')
    expect(declaredStyle(dialog).transform).toBe('translateY(0)')
  })
})
