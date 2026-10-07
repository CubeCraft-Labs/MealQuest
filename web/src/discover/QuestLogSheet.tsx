import { Trash2 } from 'lucide-react'
import { useId, useState } from 'react'
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
function details(dinner: QuestLogDinner): string | null {
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

  function cancelRemove() {
    remove.reset()
    setRemoving(null)
  }

  return (
    <>
      <Sheet open={open} onClose={onClose} aria-labelledby={titleId}>
        <div className={styles.sheet}>
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
                      <IconButton label={`Remove ${dinner.name}`} tone="neutral" onClick={() => setRemoving(dinner)}>
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
        confirmLabel={remove.isPending ? 'Removing…' : 'Remove'}
        cancelLabel="Keep it"
        busy={remove.isPending}
        error={remove.isError ? `Couldn't remove it. ${remove.error.message}` : null}
        onCancel={cancelRemove}
        onConfirm={() => remove.mutate(removing!, { onSuccess: () => setRemoving(null) })}
      >
        It leaves your Quest log for good.
      </Confirm>
    </>
  )
}
