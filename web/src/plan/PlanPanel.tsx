import { Compass } from 'lucide-react'
import { useId, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { Link } from 'react-router'
import { useRegulars } from '../dinners/api.ts'
import { useQuestLog, type QuestLogDinner } from '../discover/api.ts'
import { DIFFICULTY_LABEL } from '../discover/DeckCard.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { Photo } from '../ui/Photo.tsx'
import { Segmented } from '../ui/Segmented.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import { useDraggable, type Float } from './drag.tsx'
import { emptyLists, planDinner, sourceNames, type Source } from './SwapSheet.tsx'
import styles from './PlanPanel.module.css'

// DRAG_HINT says how a dinner is swapped in from the panel or the strip.
const DRAG_HINT = 'Drag a dinner onto a day to swap it in.'

// KEYS_HINT says how a dinner is swapped in with the keyboard.
const KEYS_HINT = 'Press Enter or Space to pick it up, the arrow keys to choose a day, Enter to drop it on that day and Escape to cancel.'

type List = ReturnType<typeof useQuestLog>

// ListBody is a list of the panel or the strip once it has loaded, or why it
// has not: its error with Retry, skeletons while loading, or that it is
// empty.
function ListBody({ source, list, children }: { source: Source; list: List; children: (dinners: QuestLogDinner[]) => ReactNode }) {
  if (list.isError) {
    return (
      <ErrorBanner onRetry={() => void list.refetch()}>
        Couldn't load your {sourceNames[source]}. {list.error.message}
      </ErrorBanner>
    )
  }
  if (list.isPending) {
    return (
      <div aria-busy="true" aria-label={`${sourceNames[source]} loading`} className={styles.loading}>
        {Array.from({ length: 3 }, (_, i) => (
          <Skeleton key={i} className={styles.skeleton} />
        ))}
      </div>
    )
  }
  if (list.data.length === 0) return <p className={styles.empty}>{emptyLists[source]}</p>
  return children(list.data)
}

// Grip is the 6-dot drag handle of a panel row.
function Grip() {
  return (
    <span className={styles.grip} aria-hidden="true">
      {Array.from({ length: 6 }, (_, i) => (
        <span key={i} className={styles.dot} />
      ))}
    </span>
  )
}

// Draggable is a dinner of the panel or the strip, dragged onto a day to
// swap it in: a button whose content is the row or the chip. While it is
// dragged it is a dashed ghost, and with a pointer a copy of it floats
// below right of the pointer, lifted and tilted.
function Draggable({
  dinner,
  fromQuestLog,
  className,
  keysHintId,
  children,
}: {
  dinner: QuestLogDinner
  fromQuestLog: boolean
  className: string | undefined
  keysHintId: string
  children: ReactNode
}) {
  const { drag, float, props } = useDraggable({ dinner: planDinner(dinner), fromQuestLog })
  return (
    <li>
      <button
        type="button"
        className={[className, styles.draggable, drag !== null && styles.ghost].filter(Boolean).join(' ')}
        aria-describedby={keysHintId}
        {...props}
      >
        {children}
      </button>
      {float !== null && <Floating float={float} className={className}>{children}</Floating>}
    </li>
  )
}

// Floating is the dragged copy of a row or a chip, drawn over the page.
function Floating({ float, className, children }: { float: Float; className: string | undefined; children: ReactNode }) {
  return createPortal(
    <div
      aria-hidden="true"
      data-testid="drag-float"
      className={[className, styles.floating].filter(Boolean).join(' ')}
      style={{ left: float.left, top: float.top, width: float.width }}
    >
      {children}
    </div>,
    document.body,
  )
}

// KeysHint is the keyboard's way to swap a dinner in, describing each
// draggable.
function KeysHint({ id }: { id: string }) {
  return (
    <p id={id} hidden>
      {KEYS_HINT}
    </p>
  )
}

// PlanPanel is desktop's right panel (mock 2d): the Quest log and the
// Regulars, segmented "Quest log · N | Regulars · N", each dinner with its
// 48 px photo, name, difficulty and grip, and a link to swipe more Side
// quests.
export function PlanPanel() {
  const questLog = useQuestLog()
  const regulars = useRegulars()
  const [source, setSource] = useState<Source>('quest_log')
  const lists = { quest_log: questLog, regulars }
  const keysHintId = useId()

  const label = (s: Source) => {
    const dinners = lists[s].data
    return dinners === undefined ? sourceNames[s] : `${sourceNames[s]} · ${dinners.length}`
  }

  return (
    <aside aria-label="Quest log and Regulars" className={styles.panel}>
      <Segmented
        label="Dinners to swap in"
        options={[
          { value: 'quest_log', label: label('quest_log') },
          { value: 'regulars', label: label('regulars') },
        ]}
        value={source}
        onValueChange={setSource}
      />
      <p className={styles.hint}>{DRAG_HINT}</p>
      <KeysHint id={keysHintId} />
      <ListBody source={source} list={lists[source]}>
        {(dinners) => (
          <ul aria-label={sourceNames[source]} className={styles.rows}>
            {dinners.map((dinner) => (
              <Draggable
                key={dinner.id}
                dinner={dinner}
                fromQuestLog={source === 'quest_log'}
                className={styles.row}
                keysHintId={keysHintId}
              >
                {/* The row's name already says what the photo shows. */}
                <span aria-hidden="true">
                  <Photo id={dinner.photo_id} name={dinner.name} radius="var(--radius-pill)" sizes="48px" className={styles.rowPhoto} />
                </span>
                <span className={styles.rowText}>
                  <span className={styles.rowName}>{dinner.name}</span>
                  {dinner.difficulty !== null && <span className={styles.rowMeta}>{DIFFICULTY_LABEL[dinner.difficulty]}</span>}
                </span>
                <Grip />
              </Draggable>
            ))}
          </ul>
        )}
      </ListBody>
      <Link to="/discover" className={styles.more}>
        <Icon icon={Compass} size="inline" />
        Swipe more side quests
      </Link>
    </aside>
  )
}

// QuestLogStrip is tablet's Quest log under the week's grid (mock 2c): each
// dinner a chip with its 28 px photo and name, dragged as the panel's rows
// are.
export function QuestLogStrip() {
  const questLog = useQuestLog()
  const keysHintId = useId()
  const title = questLog.data === undefined ? sourceNames.quest_log : `${sourceNames.quest_log} · ${questLog.data.length}`
  return (
    <section aria-label={sourceNames.quest_log} className={styles.strip}>
      <div className={styles.stripHeader}>
        <h2 className={styles.stripTitle}>{title}</h2>
        <p className={styles.stripHint}>{DRAG_HINT}</p>
      </div>
      <KeysHint id={keysHintId} />
      <ListBody source="quest_log" list={questLog}>
        {(dinners) => (
          <ul aria-label={`${sourceNames.quest_log} dinners`} className={styles.chips}>
            {dinners.map((dinner) => (
              <Draggable key={dinner.id} dinner={dinner} fromQuestLog className={styles.chip} keysHintId={keysHintId}>
                <span aria-hidden="true">
                  <Photo id={dinner.photo_id} name={dinner.name} radius="var(--radius-pill)" sizes="28px" className={styles.chipPhoto} />
                </span>
                {dinner.name}
              </Draggable>
            ))}
          </ul>
        )}
      </ListBody>
    </section>
  )
}
