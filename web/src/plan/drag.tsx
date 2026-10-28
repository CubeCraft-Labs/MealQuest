import {
  createContext,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent,
  type ReactNode,
} from 'react'
import { planDayName, type PlanDay, type PlanDinner, type Swap } from './api.ts'
import { weekdayName } from './week.ts'
import styles from './drag.module.css'

// Pickup is a dinner picked up from the plan panel or the Quest-log strip,
// and whether it is a Quest-log dinner.
export type Pickup = { dinner: PlanDinner; fromQuestLog: boolean }

// Board is the week's grid as a drag drops onto it: its days, whether a
// swap of the week is pending, and how a dinner dropped on a day is swapped
// in.
export type Board = { days: PlanDay[]; pending: boolean; drop: (swap: Swap) => void }

// Drag is the dinner being dragged, by pointer or keyboard, and the date of
// the day it would replace, if any.
export type Drag = { pickup: Pickup; keyboard: boolean; date: string | null }

type PlanDrag = {
  drag: Drag | null
  board: Board | null
  setBoard: (board: Board | null) => void
  message: string
  // start picks a dinner up, unless it cannot be dropped: there is no grid,
  // a swap of the week is pending, or the dinner is already this week. It
  // says whether it did.
  start: (pickup: Pickup, keyboard: boolean) => boolean
  // over moves the drag onto the day of date, or off every day (null).
  over: (date: string | null) => void
  // step moves a keyboard drag to the previous (-1) or next (1) day.
  step: (by: -1 | 1) => void
  // end drops the dinner on the day of date, or cancels the drag when date
  // is null. It says whether the dinner was dropped.
  end: (date: string | null) => boolean
  cancel: () => void
}

const PlanDragContext = createContext<PlanDrag | null>(null)

function usePlanDrag(): PlanDrag {
  const context = useContext(PlanDragContext)
  if (context === null) throw new Error('usePlanDrag must be used inside PlanDragProvider')
  return context
}

// replaces says which dinner dropping on day replaces: "Friday: replaces
// Pizza night."
function replaces(day: PlanDay): string {
  return `${weekdayName(day.date)}: replaces ${planDayName(day)}.`
}

// PlanDragProvider holds the drag of a dinner from the plan panel or the
// Quest-log strip onto a day of the week's grid, which the grid registers as
// the Board. Each step of a drag is announced to screen readers.
export function PlanDragProvider({ children }: { children: ReactNode }) {
  const [board, setBoard] = useState<Board | null>(null)
  const [drag, setDrag] = useState<Drag | null>(null)
  const [message, setMessage] = useState('')
  // The drag as last set, read by handlers that run before a render: a blur
  // the drop's focus move causes must not cancel the drag it just ended.
  const current = useRef<Drag | null>(null)

  const value = useMemo((): PlanDrag => {
    function set(next: Drag | null) {
      current.current = next
      setDrag(next)
    }
    function dayOf(date: string | null): PlanDay | undefined {
      return board?.days.find((day) => day.date === date)
    }
    function cancel() {
      const now = current.current
      if (now === null) return
      set(null)
      setMessage(`Cancelled. ${now.pickup.dinner.name} was not swapped in.`)
    }
    return {
      drag,
      board,
      setBoard,
      message,
      start(pickup, keyboard) {
        const { name } = pickup.dinner
        if (board === null) {
          setMessage(`There is no plan this week to swap ${name} into.`)
          return false
        }
        if (board.pending) {
          setMessage(`A swap is still being saved. Pick ${name} up once it is done.`)
          return false
        }
        if (board.days.some((day) => day.dinner?.id === pickup.dinner.id)) {
          setMessage(`${name} is already this week.`)
          return false
        }
        // A keyboard drag starts on Monday; a pointer drag on no day.
        const first = board.days[0]!
        set({ pickup, keyboard, date: keyboard ? first.date : null })
        setMessage(
          keyboard
            ? `Picked up ${name}. ${replaces(first)} Use the arrow keys to choose a day, Enter to drop, Escape to cancel.`
            : `Picked up ${name}.`,
        )
        return true
      },
      over(date) {
        const now = current.current
        if (now === null || now.date === date) return
        set({ ...now, date })
      },
      step(by) {
        const now = current.current
        if (now === null || board === null) return
        const at = board.days.findIndex((day) => day.date === now.date)
        const next = board.days[Math.min(Math.max(at + by, 0), board.days.length - 1)]!
        set({ ...now, date: next.date })
        setMessage(replaces(next))
      },
      end(date) {
        const now = current.current
        if (now === null) return false
        const day = dayOf(date)
        if (day === undefined || board === null) {
          cancel()
          return false
        }
        set(null)
        if (board.pending) {
          setMessage(`A swap is still being saved. ${now.pickup.dinner.name} was not swapped in.`)
          return false
        }
        board.drop({ date: day.date, dinner: now.pickup.dinner, fromQuestLog: now.pickup.fromQuestLog })
        setMessage(`Dropped ${now.pickup.dinner.name} on ${weekdayName(day.date)}, replacing ${planDayName(day)}.`)
        return true
      },
      cancel,
    }
  }, [board, drag, message])

  return <PlanDragContext.Provider value={value}>{children}</PlanDragContext.Provider>
}

// DragAnnouncer is the polite live region a drag's steps are announced in.
export function DragAnnouncer() {
  const { message } = usePlanDrag()
  return (
    <p aria-live="polite" aria-atomic="true" data-testid="drag-announcer" className={styles.announcer}>
      {message}
    </p>
  )
}

// DropBoard registers the week's grid as the Board while it is shown.
export function DropBoard({ days, pending, drop }: Board) {
  const { setBoard } = usePlanDrag()
  // drop is a new function on every render: the Board calls the latest.
  const latestDrop = useRef(drop)
  useLayoutEffect(() => {
    latestDrop.current = drop
  })
  useLayoutEffect(() => {
    setBoard({ days, pending, drop: (swap) => latestDrop.current(swap) })
    return () => setBoard(null)
  }, [setBoard, days, pending])
  return null
}

// useDropTarget is the drag over the day of date, if it is over that day.
export function useDropTarget(date: string): Drag | null {
  const { drag } = usePlanDrag()
  return drag !== null && drag.date === date ? drag : null
}

// DROP_DATE is the attribute naming the date of a day a dinner drops on.
export const DROP_DATE = 'data-drop-date'

// SWAP_BUTTON marks a day's swap button, which focus moves to once a dinner
// dropped on the day: the day's recipe button comes before it.
export const SWAP_BUTTON = 'data-swap-button'

// dateAt is the date of the day under the point (x, y), or null when it is
// over no day.
function dateAt(x: number, y: number): string | null {
  return document.elementFromPoint(x, y)?.closest(`[${DROP_DATE}]`)?.getAttribute(DROP_DATE) ?? null
}

// A press becomes a drag once the pointer has moved this far, so a tap is
// not one.
const DRAG_THRESHOLD = 4

// The dragged copy floats this far below and to the right of the pointer,
// as a drag image does, so it stays clear of the "Drop to swap" text
// centred in the day the pointer is over. Where it would leave the
// viewport it flips to the left of the pointer, or above it.
const FLOAT_OFFSET = 48

// Press is a pointer held on a draggable: its id, where it went down, the
// draggable's size, and whether it has become a drag.
type Press = { pointerId: number; x: number; y: number; width: number; height: number; dragging: boolean }

// Float is where the dragged copy of a draggable is drawn, and how wide.
export type Float = { left: number; top: number; width: number }

// floatAt is where the dragged copy of the held draggable is drawn for the
// pointer at (x, y): offset below right of it, flipped where that would
// leave the viewport.
function floatAt(held: Press, x: number, y: number): Float {
  const right = x + FLOAT_OFFSET
  const below = y + FLOAT_OFFSET
  return {
    left: right + held.width > window.innerWidth ? x - FLOAT_OFFSET - held.width : right,
    top: below + held.height > window.innerHeight ? y - FLOAT_OFFSET - held.height : below,
    width: held.width,
  }
}

// useDraggable makes a panel row or a strip chip a draggable dinner. With a
// pointer it is pressed, captured and moved past DRAG_THRESHOLD to pick it
// up, its copy floats with the pointer, and letting go over a day drops it
// there, anywhere else cancels. With the keyboard Enter or Space picks it
// up onto Monday, the arrow keys choose the day, Enter drops it and
// Escape, or leaving it, cancels; a drop moves focus to the day's swap
// button, as a Quest-log dinner leaves the Quest log once planned.
export function useDraggable(pickup: Pickup) {
  const plan = usePlanDrag()
  const [float, setFloat] = useState<Float | null>(null)
  const press = useRef<Press | null>(null)
  const drag = plan.drag !== null && plan.drag.pickup.dinner.id === pickup.dinner.id ? plan.drag : null
  const disabled = plan.drag === null && (plan.board === null || plan.board.pending || plan.board.days.some((day) => day.dinner?.id === pickup.dinner.id))

  // A draggable that goes mid-drag, as on crossing to mobile, cancels it;
  // once the drag has ended, cancel does nothing.
  const { cancel } = plan
  const dragging = drag !== null
  const latestCancel = useRef(cancel)
  useLayoutEffect(() => {
    latestCancel.current = cancel
  })
  useEffect(() => {
    if (!dragging) return
    return () => latestCancel.current()
  }, [dragging])

  function release() {
    press.current = null
    setFloat(null)
  }

  function onPointerDown(event: PointerEvent<HTMLElement>) {
    if (press.current !== null || plan.drag !== null || !event.isPrimary || event.button !== 0) return
    const rect = event.currentTarget.getBoundingClientRect()
    event.currentTarget.setPointerCapture(event.pointerId)
    press.current = {
      pointerId: event.pointerId,
      x: event.clientX,
      y: event.clientY,
      width: rect.width,
      height: rect.height,
      dragging: false,
    }
  }

  function onPointerMove(event: PointerEvent<HTMLElement>) {
    const held = press.current
    if (held === null || held.pointerId !== event.pointerId) return
    if (!held.dragging) {
      if (Math.hypot(event.clientX - held.x, event.clientY - held.y) < DRAG_THRESHOLD) return
      if (!plan.start(pickup, false)) {
        press.current = null
        return
      }
      held.dragging = true
    }
    setFloat(floatAt(held, event.clientX, event.clientY))
    plan.over(dateAt(event.clientX, event.clientY))
  }

  function onPointerUp(event: PointerEvent<HTMLElement>) {
    const held = press.current
    if (held === null || held.pointerId !== event.pointerId) return
    release()
    if (held.dragging) plan.end(dateAt(event.clientX, event.clientY))
  }

  // A cancel, or losing the capture before the release, cancels the drag.
  // After a release the press is already over, so this ends only a drag
  // whose capture was lost before it.
  function onPointerCancel(event: PointerEvent<HTMLElement>) {
    const held = press.current
    if (held === null || held.pointerId !== event.pointerId) return
    release()
    if (held.dragging) plan.cancel()
  }

  function onKeyDown(event: KeyboardEvent<HTMLElement>) {
    const button = event.currentTarget
    if (drag === null) {
      if (event.key !== 'Enter' && event.key !== ' ') return
      event.preventDefault()
      if (plan.drag === null) plan.start(pickup, true)
      return
    }
    if (event.key === 'Escape') {
      event.preventDefault()
      release()
      plan.cancel()
      return
    }
    if (!drag.keyboard) return
    // Space picked it up and does not drop it; it does not scroll the page.
    if (event.key === 'ArrowLeft' || event.key === 'ArrowUp') plan.step(-1)
    else if (event.key === 'ArrowRight' || event.key === 'ArrowDown') plan.step(1)
    else if (event.key === 'Enter') drop(button.ownerDocument, drag.date)
    else if (event.key !== ' ') return
    event.preventDefault()
  }

  // drop ends a keyboard drag on the day of date and, once dropped, moves
  // focus to that day's swap button.
  function drop(document: Document, date: string | null) {
    if (!plan.end(date)) return
    const swapButton = document.querySelector<HTMLElement>(`[${DROP_DATE}="${date}"] [${SWAP_BUTTON}]`)
    if (swapButton === null) throw new Error(`The day ${date} dropped on has no swap button to focus.`)
    swapButton.focus()
  }

  // Leaving a keyboard drag cancels it.
  function onBlur() {
    if (drag !== null && drag.keyboard) plan.cancel()
  }

  return {
    drag,
    float,
    props: {
      'aria-disabled': disabled || undefined,
      onPointerDown,
      onPointerMove,
      onPointerUp,
      onPointerCancel,
      onLostPointerCapture: onPointerCancel,
      onKeyDown,
      onBlur,
    },
  }
}
