import { Trash2 } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import { useNavigate } from 'react-router'
import { Confirm } from '../settings/LeaveGuard.tsx'
import { Icon } from '../ui/Icon.tsx'
import { IconButton } from '../ui/IconButton.tsx'
import { ListRow } from '../ui/ListRow.tsx'
import { Photo } from '../ui/Photo.tsx'
import { Sheet } from '../ui/Sheet.tsx'
import { useRemoveFromQuestLog, type QuestLogDinner } from './api.ts'
import { DIFFICULTY_LABEL } from './DeckCard.tsx'
import styles from './QuestLogSheet.module.css'

export type QuestLogSheetProps = {
  open: boolean
  // The Quest log, as useQuestLog reads it.
  dinners: QuestLogDinner[]
  onClose: () => void
}

// details is a Quest-log dinner's "difficulty · minutes" line, or null when
// neither is known.
export function details(dinner: QuestLogDinner): string | null {
  const parts = [
    dinner.difficulty !== null && DIFFICULTY_LABEL[dinner.difficulty],
    dinner.minutes_total !== null && `${dinner.minutes_total} min`,
  ].filter((part): part is string => typeof part === 'string')
  return parts.length === 0 ? null : parts.join(' · ')
}

// QuestLogSheet is the Quest log as a bottom sheet: each dinner with its
// washed photo, name, difficulty and minutes. A row opens the dinner; its
// remove button, once confirmed, archives the dinner.
export function QuestLogSheet({ open, dinners, onClose }: QuestLogSheetProps) {
  const titleId = useId()
  const navigate = useNavigate()
  const remove = useRemoveFromQuestLog()
  // The dinner whose removal is being confirmed.
  const [removing, setRemoving] = useState<QuestLogDinner | null>(null)
  // The dinner just removed and its place in the list, until focus has
  // moved on from it.
  const removed = useRef<{ id: string; index: number } | null>(null)
  const removeButtons = useRef(new Map<string, HTMLButtonElement>())
  const sheetRef = useRef<HTMLDivElement>(null)
  // Whether a removal has been sent and not yet settled. The ref is set as
  // Remove is activated, so a dismissal or a second Remove that arrives before
  // the next render is already refused; the state renders it.
  const sending = useRef(false)
  const [sendingShown, setSendingShown] = useState(false)

  // Once a removal's confirmation has closed and the dinner has left the list,
  // focus goes to the remove button of the dinner now in its place, or of the
  // one before it when it was the last, or to the Quest log sheet itself when
  // none is left. Otherwise it would be left on the removed dinner's button,
  // which the confirmation hands it back to, and which is then gone.
  useEffect(() => {
    const done = removed.current
    if (removing !== null || done === null || dinners.some((d) => d.id === done.id)) return
    removed.current = null
    const next = dinners[Math.min(done.index, dinners.length - 1)]
    if (next) removeButtons.current.get(next.id)!.focus()
    else sheetRef.current!.closest<HTMLElement>('[role="dialog"]')!.focus()
  }, [removing, dinners])

  function setSending(value: boolean) {
    sending.current = value
    setSendingShown(value)
  }

  function cancelRemove() {
    if (sending.current) return
    remove.reset()
    setRemoving(null)
  }

  function confirmRemove() {
    if (sending.current) return
    setSending(true)
    remove.mutate(removing!, {
      onSuccess: (_data, dinner) => {
        removed.current = { id: dinner.id, index: dinners.findIndex((d) => d.id === dinner.id) }
        setRemoving(null)
      },
      onSettled: () => setSending(false),
    })
  }

  return (
    <>
      <Sheet open={open} onClose={onClose} aria-labelledby={titleId}>
        <div ref={sheetRef} className={styles.sheet}>
          <h2 id={titleId} className={styles.title}>
            Quest log
          </h2>
          {dinners.length === 0 ? (
            <p className={styles.empty}>Your Quest log is empty — swipe right on a side quest to add one.</p>
          ) : (
            <ul aria-label="Quest log" className={styles.rows}>
              {dinners.map((dinner) => (
                <li key={dinner.id}>
                  <ListRow
                    leading={
                      // The row's name already says what the photo shows.
                      <span aria-hidden="true">
                        <Photo id={dinner.photo_id} name={dinner.name} radius="var(--radius-md)" sizes="48px" className={styles.photo} />
                      </span>
                    }
                    title={dinner.name}
                    meta={details(dinner)}
                    onClick={() => navigate(`/dinner/${encodeURIComponent(dinner.id)}`)}
                    trailing={
                      <IconButton
                        ref={(button) => {
                          if (button) removeButtons.current.set(dinner.id, button)
                          else removeButtons.current.delete(dinner.id)
                        }}
                        label={`Remove ${dinner.name}`}
                        tone="neutral"
                        onClick={() => setRemoving(dinner)}
                      >
                        <Icon icon={Trash2} />
                      </IconButton>
                    }
                  />
                </li>
              ))}
            </ul>
          )}
        </div>
      </Sheet>
      <Confirm
        open={removing !== null}
        title={`Remove ${removing?.name ?? ''}?`}
        confirmLabel={sendingShown ? 'Removing…' : 'Remove'}
        cancelLabel="Keep it"
        busy={sendingShown}
        // Once the removal is sent, it stands: the confirmation stays until it settles.
        dismissible={!sendingShown}
        error={remove.isError ? `Couldn't remove it. ${remove.error.message}` : null}
        onCancel={cancelRemove}
        onConfirm={confirmRemove}
      >
        It leaves your Quest log for good.
      </Confirm>
    </>
  )
}
