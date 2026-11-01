import { useId, useState } from 'react'
import { useRegulars } from '../dinners/api.ts'
import { useQuestLog, type QuestLogDinner } from '../discover/api.ts'
import { details } from '../discover/QuestLogSheet.tsx'
import { Button } from '../ui/Button.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { ListRow } from '../ui/ListRow.tsx'
import { Photo } from '../ui/Photo.tsx'
import { Segmented } from '../ui/Segmented.tsx'
import { Sheet } from '../ui/Sheet.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import { Tag } from '../ui/Tag.tsx'
import { TextInput } from '../ui/TextInput.tsx'
import { planDayName, type PlanDay, type PlanDinner, type Swap } from './api.ts'
import { weekdayName } from './week.ts'
import styles from './SwapSheet.module.css'

// Source is a list a dinner is swapped in from.
export type Source = 'quest_log' | 'regulars'

export const sourceNames: Record<Source, string> = { quest_log: 'Quest log', regulars: 'Regulars' }

export const emptyLists: Record<Source, string> = {
  quest_log: 'Your Quest log is empty — swipe right on a Side quest to add one.',
  regulars: 'You have no Regulars yet.',
}

// planDinner is a dinner listed in the Quest log or the Regulars as it is
// planned on a day.
export function planDinner(dinner: QuestLogDinner, fromQuestLog: boolean): PlanDinner {
  return {
    id: dinner.id,
    name: dinner.name,
    status: fromQuestLog ? 'quest_log' : 'regular',
    photo_id: dinner.meal_source === 'cook' ? dinner.photo_id : undefined,
    minutes: dinner.minutes_total,
    method: dinner.method,
    meal_source: dinner.meal_source,
  }
}

// cookedDinners are the dinners a plan can take: delivery and dining out
// are not planned.
export function cookedDinners(dinners: QuestLogDinner[]): QuestLogDinner[] {
  return dinners.filter((dinner) => dinner.meal_source === 'cook')
}

export type SwapSheetProps = {
  // The day being swapped.
  day: PlanDay
  // The ids of the dinners already on the week's plan.
  planned: ReadonlySet<string>
  onSwap: (swap: Swap) => void
  onClose: () => void
}

// SwapSheet is the mobile swap sheet: the Quest log and the Regulars, as
// desktop's plan panel lists them, segmented "Quest log · N | Regulars · N",
// each dinner with its 48 px photo, name, difficulty and minutes. Choosing a
// dinner swaps it into the day; a dinner already this week can't be chosen.
export function SwapSheet({ day, planned, onSwap, onClose }: SwapSheetProps) {
  const titleId = useId()
  const questLog = useQuestLog()
  const regulars = useRegulars()
  const [source, setSource] = useState<Source>('quest_log')
  const [label, setLabel] = useState(day.manual_label ?? '')
  const named = planDayName(day)
  const occupied = day.dinner !== null || day.manual_label !== null || day.meal_source !== 'cook'
  const lists = { quest_log: questLog, regulars }
  const list = lists[source]

  const sourceLabel = (s: Source) => {
    const dinners = lists[s].data
    const cooked = dinners === undefined ? undefined : cookedDinners(dinners)
    return cooked === undefined ? sourceNames[s] : `${sourceNames[s]} · ${cooked.length}`
  }

  let body
  if (list.isError) {
    body = (
      <ErrorBanner onRetry={() => void list.refetch()}>
        Couldn't load your {sourceNames[source]}. {list.error.message}
      </ErrorBanner>
    )
  } else if (list.isPending) {
    body = (
      <div aria-busy="true" aria-label={`${sourceNames[source]} loading`} className={styles.rows}>
        {Array.from({ length: 3 }, (_, i) => (
          <Skeleton key={i} className={styles.skeletonRow} />
        ))}
      </div>
    )
  } else if (cookedDinners(list.data).length === 0) {
    body = (
      <p className={styles.empty}>
        {list.data.length === 0 ? emptyLists[source] : 'Delivery and dining out are not planned.'}
      </p>
    )
  } else {
    body = (
      <ul aria-label={sourceNames[source]} className={styles.rows}>
        {cookedDinners(list.data).map((dinner: QuestLogDinner) => {
          const already = planned.has(dinner.id)
          return (
            <li key={dinner.id}>
              <ListRow
                className={already ? styles.already : undefined}
                leading={
                  // The row's name already says what the photo shows.
                  <span aria-hidden="true">
                    <Photo
                      id={dinner.meal_source === 'cook' ? dinner.photo_id : undefined}
                      name={dinner.name}
                      radius="var(--radius-md)"
                      sizes="48px"
                      className={styles.photo}
                    />
                  </span>
                }
                title={dinner.name}
                meta={details(dinner)}
                // A dinner already this week is no button: it can't be chosen.
                onClick={
                  already
                    ? undefined
                    : () =>
                        onSwap({ date: day.date, dinner: planDinner(dinner, source === 'quest_log'), fromQuestLog: source === 'quest_log' })
                }
                trailing={already && <Tag>Already this week</Tag>}
              />
            </li>
          )
        })}
      </ul>
    )
  }

  return (
    <Sheet open onClose={onClose} aria-labelledby={titleId}>
      <div className={styles.sheet}>
        <div>
          <h2 id={titleId} className={styles.title}>
            Swap {weekdayName(day.date)}
          </h2>
          <p className={styles.replaces}>{occupied ? `Replaces ${named}` : 'Nothing planned this night'}</p>
        </div>
        <div className={styles.manual}>
          <TextInput
            label="One night only"
            value={label}
            maxLength={120}
            placeholder="mummy dogs"
            onChange={(event) => setLabel(event.target.value)}
          />
          <Button variant="outline" disabled={label.trim() === ''} onClick={() => onSwap({ date: day.date, manual_label: label.trim() })}>
            Use this name
          </Button>
          {occupied && (
            <Button variant="outline" onClick={() => onSwap({ date: day.date, clear: true })}>
              Clear this night
            </Button>
          )}
        </div>
        <Segmented
          label="Swap in from"
          options={[
            { value: 'quest_log', label: sourceLabel('quest_log') },
            { value: 'regulars', label: sourceLabel('regulars') },
          ]}
          value={source}
          onValueChange={setSource}
        />
        {body}
      </div>
    </Sheet>
  )
}
