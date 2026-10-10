import { CalendarDays, ChefHat, Compass, Repeat, ShoppingCart, type LucideIcon } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import { useNavigate } from 'react-router'
import { ago } from '../plan/PlanPage.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { ListRow } from '../ui/ListRow.tsx'
import { Sheet } from '../ui/Sheet.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import { useMarkUpdatesSeen, useUpdates, type Update, type UpdateType } from './api.ts'
import styles from './UpdatesSheet.module.css'

const ICONS: Record<UpdateType, LucideIcon> = {
  plan_ready: CalendarDays,
  list_ready: ShoppingCart,
  day_of_reminder: ChefHat,
  new_suggestions: Compass,
  rotation_review: Repeat,
}

// UpdateRow is one update: its icon, title and body, and how long ago it
// came. A tap opens its url.
function UpdateRow({ update, now, onOpen }: { update: Update; now: Date; onOpen: (url: string) => void }) {
  return (
    <li>
      <ListRow
        leading={<Icon icon={ICONS[update.type]} className={styles.icon} />}
        title={update.title}
        meta={update.body === '' ? undefined : update.body}
        trailing={
          <time dateTime={update.created_at} className={styles.time}>
            {ago(new Date(update.created_at), now)}
          </time>
        }
        onClick={() => onOpen(update.url)}
      />
    </li>
  )
}

// Updates lists the Recent updates, newest first, and marks them seen
// through the newest once they are shown, so the bell's dot clears.
function Updates({ onOpen }: { onOpen: (url: string) => void }) {
  const updates = useUpdates()
  const markSeen = useMarkUpdatesSeen()
  const { mutate } = markSeen
  const newest = updates.data?.[0]?.created_at
  // The instant the relative times count from, fixed while the sheet is
  // open.
  const [now] = useState(() => new Date())

  useEffect(() => {
    if (newest !== undefined) mutate(newest)
  }, [newest, mutate])

  if (updates.isError) {
    return (
      <ErrorBanner onRetry={() => void updates.refetch()}>Couldn't load your updates. {updates.error.message}</ErrorBanner>
    )
  }
  if (updates.isPending) {
    return (
      <div aria-busy="true" aria-label="Updates loading" className={styles.rows}>
        {Array.from({ length: 3 }, (_, i) => (
          <Skeleton key={i} className={styles.skeletonRow} />
        ))}
      </div>
    )
  }
  if (updates.data.length === 0) {
    return <p className={styles.empty}>No updates yet. Plans, Supply runs and new Side quests show up here.</p>
  }
  return (
    <>
      {markSeen.isError && newest !== undefined && (
        <ErrorBanner onRetry={() => mutate(newest)}>
          Couldn't mark your updates as seen. {markSeen.error.message}
        </ErrorBanner>
      )}
      <ul aria-label="Recent updates" className={styles.rows}>
        {updates.data.map((update, i) => (
          <UpdateRow key={`${i} ${update.created_at}`} update={update} now={now} onOpen={onOpen} />
        ))}
      </ul>
    </>
  )
}

export type UpdatesSheetProps = {
  open: boolean
  onClose: () => void
}

// UpdatesSheet is the Recent updates sheet the Home bell opens: the last
// ten notification events with how long ago each came, each opening the
// screen it is about. Opening it marks them seen.
export function UpdatesSheet({ open, onClose }: UpdatesSheetProps) {
  const titleId = useId()
  const navigate = useNavigate()
  const openUpdate = (url: string) => {
    onClose()
    void navigate(url)
  }
  return (
    <Sheet open={open} onClose={onClose} aria-labelledby={titleId}>
      <div className={styles.sheet}>
        <h2 id={titleId} className={styles.title}>
          Recent updates
        </h2>
        <Updates onOpen={openUpdate} />
      </div>
    </Sheet>
  )
}
