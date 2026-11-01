import { Ellipsis } from 'lucide-react'
import { useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent } from 'react'
import { createPortal } from 'react-dom'
import { MEAL_SOURCE_LABEL } from '../dinners/api.ts'
import { Icon } from '../ui/Icon.tsx'
import { IconButton } from '../ui/IconButton.tsx'
import type { PlanDay, Swap } from './api.ts'
import { weekdayName } from './week.ts'
import styles from './EveningMenu.module.css'

type FocusAt = 'first' | 'last' | 'checked'

export type EveningMenuProps = {
  day: PlanDay
  disabled: boolean
  className?: string
  onChoose: (swap: Swap) => void
}

function choices(menu: HTMLElement): HTMLButtonElement[] {
  return Array.from(menu.querySelectorAll<HTMLButtonElement>('button:not(:disabled)'))
}

// EveningMenu is the day's own control for Delivery, Dining out and clearing
// the night. It sits on the tile or card, so the evening changes without
// opening the swap sheet. Choosing a cooked dinner stays the swap sheet.
export function EveningMenu({ day, disabled, className, onChoose }: EveningMenuProps) {
  const menuId = useId()
  const buttonRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const focusAt = useRef<FocusAt>('checked')
  const [open, setOpen] = useState(false)
  const dayName = weekdayName(day.date)
  const label = `Evening for ${dayName}`
  const clearable = day.meal_source !== 'cook' || day.dinner !== null || day.manual_label !== null

  function openMenu(at: FocusAt) {
    focusAt.current = at
    setOpen(true)
  }

  function close(restore: boolean) {
    setOpen(false)
    if (restore) buttonRef.current?.focus()
  }

  function choose(swap: Swap) {
    close(true)
    onChoose(swap)
  }

  useLayoutEffect(() => {
    if (!open) return
    const menu = menuRef.current
    const button = buttonRef.current
    if (!menu || !button) return
    const rect = button.getBoundingClientRect()
    const width = menu.offsetWidth
    const height = menu.offsetHeight
    const margin = 8
    const left = Math.min(Math.max(margin, rect.right - width), Math.max(margin, window.innerWidth - width - margin))
    let top = rect.bottom + 4
    if (top + height > window.innerHeight - margin) top = Math.max(margin, rect.top - height - 4)
    menu.style.left = `${left}px`
    menu.style.top = `${top}px`
    const items = choices(menu)
    const checked = items.findIndex((item) => item.getAttribute('aria-checked') === 'true')
    const index = focusAt.current === 'last' ? items.length - 1 : focusAt.current === 'first' || checked < 0 ? 0 : checked
    items[index]?.focus()
  }, [open])

  useEffect(() => {
    if (!open) return
    function onPointerDown(event: PointerEvent) {
      const target = event.target
      if (!(target instanceof Node)) return
      if (buttonRef.current?.contains(target) || menuRef.current?.contains(target)) return
      setOpen(false)
    }
    document.addEventListener('pointerdown', onPointerDown)
    return () => document.removeEventListener('pointerdown', onPointerDown)
  }, [open])

  function onButtonKeyDown(event: KeyboardEvent<HTMLButtonElement>) {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
    event.preventDefault()
    openMenu(event.key === 'ArrowUp' ? 'last' : 'first')
  }

  function onMenuKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const menu = menuRef.current
    if (!menu) return
    const items = choices(menu)
    const current = items.indexOf(document.activeElement as HTMLButtonElement)
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      const step = event.key === 'ArrowDown' ? 1 : -1
      const next = items[(current + step + items.length) % items.length]
      next?.focus()
      return
    }
    if (event.key === 'Home' || event.key === 'End') {
      event.preventDefault()
      items[event.key === 'Home' ? 0 : items.length - 1]?.focus()
      return
    }
    if (event.key === 'Escape') {
      event.preventDefault()
      event.stopPropagation()
      close(true)
      return
    }
    if (event.key === 'Tab') {
      event.preventDefault()
      close(true)
      return
    }
    if (event.key !== 'Enter' && event.key !== ' ') return
    event.preventDefault()
    const active = document.activeElement
    if (active instanceof HTMLButtonElement && menu.contains(active)) active.click()
  }

  return (
    <>
      <IconButton
        ref={buttonRef}
        label={label}
        className={className}
        disabled={disabled}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={menuId}
        onClick={() => (open ? close(false) : openMenu('checked'))}
        onKeyDown={onButtonKeyDown}
      >
        <Icon icon={Ellipsis} />
      </IconButton>
      {open &&
        createPortal(
          <div ref={menuRef} id={menuId} role="menu" aria-label={label} className={styles.menu} onKeyDown={onMenuKeyDown}>
            <button
              type="button"
              role="menuitemradio"
              aria-checked={day.meal_source === 'delivery'}
              tabIndex={-1}
              className={styles.choice}
              onClick={() => choose({ date: day.date, meal_source: 'delivery' })}
            >
              {MEAL_SOURCE_LABEL.delivery}
            </button>
            <button
              type="button"
              role="menuitemradio"
              aria-checked={day.meal_source === 'dining_out'}
              tabIndex={-1}
              className={styles.choice}
              onClick={() => choose({ date: day.date, meal_source: 'dining_out' })}
            >
              {MEAL_SOURCE_LABEL.dining_out}
            </button>
            <button type="button" role="menuitem" tabIndex={-1} className={styles.choice} disabled={!clearable} onClick={() => choose({ date: day.date, clear: true })}>
              Clear this night
            </button>
          </div>,
          document.body,
        )}
    </>
  )
}
