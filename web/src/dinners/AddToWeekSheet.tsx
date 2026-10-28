import { useId, useState } from 'react'
import { Link } from 'react-router'
import { usePlan, useWeekSwap, type PlanDinner, type Swap } from '../plan/api.ts'
import { cooking } from '../plan/PlanPage.tsx'
import swapStyles from '../plan/SwapSheet.module.css'
import { addDays, dayOfMonth, localNow, weekdayName, weekLabel, weekStart as weekOf } from '../plan/week.ts'
import { useHouseholdSettings } from '../settings/api.ts'
import buttonStyles from '../ui/Button.module.css'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { ListRow } from '../ui/ListRow.tsx'
import { Photo } from '../ui/Photo.tsx'
import { Segmented } from '../ui/Segmented.tsx'
import { Sheet } from '../ui/Sheet.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import { Tag } from '../ui/Tag.tsx'
import type { DinnerDetail } from './detail.ts'
import styles from './AddToWeekSheet.module.css'

// PLAN is the Draft board, where a week with no plan is drafted.
export const PLAN = '/plan'

// Which is the week a dinner is added to: the household's current week or
// the one after.
export type Which = 'this' | 'next'

export const weekNames: Record<Which, string> = { this: 'this week', next: 'next week' }

// Added is a swap sent from the sheet: the week it went to and the swap.
export type Added = { week: string; which: Which; swap: Swap }

// planDinner is the dinner as it is planned on a day.
function planDinner(dinner: DinnerDetail): PlanDinner {
  return {
    id: dinner.id,
    name: dinner.name,
    status: dinner.status,
    photo_id: dinner.meal_source === 'cook' ? dinner.photo_id : undefined,
    minutes: dinner.minutes_total,
    method: dinner.method,
    meal_source: dinner.meal_source,
  }
}

type WeekDaysProps = {
  dinner: DinnerDetail
  week: string
  which: Which
  onAdd: (added: Added) => void
}

// WeekDays is the seven days of the week starting on week, each with its
// dinner; choosing one swaps the dinner in as the Draft board swaps a day
// (useWeekSwap). A week with no plan offers to draft it on the Draft board:
// nothing here makes a plan up. A dinner already on the week can't be added
// again, nor can one while a swap of the week is still being saved or the
// week is being pushed to Calendar.
function WeekDays({ dinner, week, which, onAdd }: WeekDaysProps) {
  const plan = usePlan(week)
  const { swap, push, refused, swapIn } = useWeekSwap(week)

  if (plan.isError) {
    return <ErrorBanner onRetry={() => void plan.refetch()}>Couldn't load {weekNames[which]}'s plan. {plan.error.message}</ErrorBanner>
  }
  if (plan.isPending) {
    return (
      <div aria-busy="true" aria-label="Plan loading" className={swapStyles.rows}>
        {Array.from({ length: 7 }, (_, i) => (
          <Skeleton key={i} className={swapStyles.skeletonRow} />
        ))}
      </div>
    )
  }
  if (plan.data === null) {
    return (
      <div className={styles.noPlan}>
        <p className={swapStyles.empty}>There's no plan for {weekNames[which]} yet, so there's no day to add it to.</p>
        <Link to={PLAN} className={[buttonStyles.button, buttonStyles.primary].join(' ')}>
          Draft {weekNames[which]} first
        </Link>
      </div>
    )
  }

  const days = plan.data.days
  const planned = days.find((day) => day.dinner.id === dinner.id)
  const choosable = planned === undefined && !refused
  function add(date: string) {
    const chosen: Swap = { date, dinner: planDinner(dinner), fromQuestLog: dinner.status === 'quest_log' }
    swapIn(chosen)
    onAdd({ week, which, swap: chosen })
  }
  return (
    <>
      {planned !== undefined ? (
        <p className={swapStyles.empty}>
          {dinner.name} is already on {weekdayName(planned.date)} {weekNames[which]}.
        </p>
      ) : swap.pending ? (
        <p className={swapStyles.empty}>A change to {weekNames[which]} is still being saved.</p>
      ) : (
        push.pending && <p className={swapStyles.empty}>The plan for {weekNames[which]} is being pushed to Calendar.</p>
      )}
      <ul aria-label={`Days of ${weekNames[which]}`} className={swapStyles.rows}>
        {days.map((day) => (
          <li key={day.date}>
            <ListRow
              className={choosable ? undefined : swapStyles.already}
              leading={
                // The row's dinner names what the photo shows.
                <span aria-hidden="true">
                  <Photo id={day.dinner.photo_id} name={day.dinner.name} radius="var(--radius-md)" sizes="48px" className={swapStyles.photo} />
                </span>
              }
              title={`${weekdayName(day.date)} ${dayOfMonth(day.date)}`}
              meta={[day.dinner.name, cooking(day.dinner)].filter(Boolean).join(' · ')}
              onClick={choosable ? () => add(day.date) : undefined}
              trailing={day === planned && <Tag>This dinner</Tag>}
            />
          </li>
        ))}
      </ul>
    </>
  )
}

export type AddToWeekSheetProps = {
  dinner: DinnerDetail
  onAdd: (added: Added) => void
  onClose: () => void
}

// AddToWeekSheet is the sheet Add to this week opens: the seven days of the
// household's current week, Monday to Sunday in its timezone, each with its
// dinner, and a toggle to next week. Choosing a day replaces its dinner.
export function AddToWeekSheet({ dinner, onAdd, onClose }: AddToWeekSheetProps) {
  const titleId = useId()
  const settings = useHouseholdSettings()
  const [which, setWhich] = useState<Which>('this')

  let body
  if (settings.isError) {
    body = (
      <ErrorBanner onRetry={() => void settings.refetch()}>
        Couldn't load the household settings. {settings.error.message}
      </ErrorBanner>
    )
  } else if (settings.isPending) {
    body = <Skeleton className={swapStyles.skeletonRow} />
  } else {
    const thisWeek = weekOf(localNow(settings.data.timezone, new Date()).date)
    const week = which === 'this' ? thisWeek : addDays(thisWeek, 7)
    body = (
      <>
        <p className={swapStyles.replaces}>
          {weekLabel(week).dates} · replaces the day's dinner
        </p>
        <Segmented
          label="Week"
          options={[
            { value: 'this', label: 'This week' },
            { value: 'next', label: 'Next week' },
          ]}
          value={which}
          onValueChange={setWhich}
        />
        <WeekDays key={week} dinner={dinner} week={week} which={which} onAdd={onAdd} />
      </>
    )
  }

  return (
    <Sheet open onClose={onClose} aria-labelledby={titleId}>
      <div className={swapStyles.sheet}>
        <h2 id={titleId} className={swapStyles.title}>
          Add {dinner.name}
        </h2>
        {body}
      </div>
    </Sheet>
  )
}
