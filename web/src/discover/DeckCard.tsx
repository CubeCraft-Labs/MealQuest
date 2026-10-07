import type { DOMAttributes } from 'react'
import { DIFFICULTIES, type Difficulty } from '../dinners/api.ts'
import { Photo } from '../ui/Photo.tsx'
import type { SideQuest } from './api.ts'
import styles from './DiscoverPage.module.css'

// DECK_DEPTH is the depths the deck shows, top card first.
export const DECK_DEPTH = [0, 1, 2] as const

export const DIFFICULTY_LABEL: Record<Difficulty, string> = { easy: 'Easy', medium: 'Medium', hard: 'Hard' }

// SwipeHandlers are the pointer handlers that drag the top card.
export type SwipeHandlers = Pick<
  DOMAttributes<HTMLElement>,
  'onPointerDown' | 'onPointerMove' | 'onPointerUp' | 'onPointerCancel' | 'onLostPointerCapture'
>

export type DeckCardProps = {
  quest: SideQuest
  // 0 is the top card; 1 and 2 stack behind it.
  depth: number
  // swipe makes the card draggable; only the top card gets it.
  swipe?: SwipeHandlers
}

// DeckCard is one Side quest in the deck. Only the top card is read out and
// reachable; the cards behind it are the stack's edge, kept rendered so they
// can be promoted. Its "Try it!" and "Pass" stamps stay hidden until a
// decision or a drag shows them, and its shade until Pass folds it (see
// motion.ts).
export function DeckCard({ quest, depth, swipe }: DeckCardProps) {
  const top = depth === 0
  const titleId = `side-quest-${quest.id}`
  return (
    <article
      className={`${styles.card} ${styles[`depth${depth}`]}${swipe === undefined ? '' : ` ${styles.draggable}`}`}
      aria-labelledby={top ? titleId : undefined}
      aria-hidden={top ? undefined : true}
      inert={!top}
      {...swipe}
    >
      <Photo id={quest.photo_id} name={quest.name} radius="24px" inset="10px" sizes="400px" className={styles.photo} />
      <div className={styles.content}>
        <h2 id={titleId} className={styles.name}>
          {quest.name}
        </h2>
        {quest.difficulty !== null && <DifficultyDots difficulty={quest.difficulty} />}
        <p className={styles.why}>
          <strong>Why this?</strong> {quest.reason}
        </p>
      </div>
      <span className={`${styles.stamp} ${styles.tryStamp}`} data-part="try-stamp" aria-hidden="true">
        Try it!
      </span>
      <span className={`${styles.stamp} ${styles.passStamp}`} data-part="pass-stamp" aria-hidden="true">
        Pass
      </span>
      <span className={styles.shade} data-part="shade" />
    </article>
  )
}

function DifficultyDots({ difficulty }: { difficulty: Difficulty }) {
  const level = DIFFICULTIES.indexOf(difficulty) + 1
  return (
    <p className={styles.difficulty}>
      <span className={styles.dots} aria-hidden="true">
        {DIFFICULTIES.map((d, i) => (
          <span key={d} data-filled={i < level} className={i < level ? styles.dotFilled : styles.dot} />
        ))}
      </span>
      {DIFFICULTY_LABEL[difficulty]}
    </p>
  )
}
