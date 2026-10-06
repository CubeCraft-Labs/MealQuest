import type { UseQueryResult } from '@tanstack/react-query'
import type { LucideIcon } from 'lucide-react'
import { Bell, Calendar, ChevronRight, Crosshair, Heart, House } from 'lucide-react'
import { useId, type ReactNode } from 'react'
import { Link } from 'react-router'
import { Card } from '../ui/Card.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import { Tag } from '../ui/Tag.tsx'
import { useBreakpoint } from '../shell/useBreakpoint.ts'
import {
  aiStatus,
  calendarStatus,
  formatClockTime,
  notificationsStatus,
  regularDinnersStatus,
  useAISettings,
  useCalendarStatus,
  useHouseholdSettings,
  useNotificationsStatus,
  useRegularDinnersCount,
} from './api.ts'
import styles from './SettingsPage.module.css'
import { SettingsTabs } from './SettingsTabs.tsx'

// Settings home: the list of settings pages on mobile (mock 1p), one page
// with sub-tabs from 768 px (mock 2e). The sub-tabs stay mounted, hidden, on
// mobile, so an unsaved draft survives the viewport crossing 768 px and its
// leave guard keeps asking before the list navigates away.
export function SettingsPage() {
  const mobile = useBreakpoint() === 'mobile'
  return (
    <>
      {mobile && <SettingsList />}
      <SettingsTabs hidden={mobile} />
    </>
  )
}

// The mobile settings list (handoff mock 1p). Each row opens its settings
// page and shows a live status line. Regular dinners, Google Calendar and
// Notifications & Google Chat read endpoints their features have not served
// yet; until then each shows its not-yet-available state (see api.ts).
function SettingsList() {
  const household = useHouseholdSettings()
  const ai = useAISettings()
  const regulars = useRegularDinnersCount()
  const calendar = useCalendarStatus()
  const notifications = useNotificationsStatus()
  const failed = [regulars, calendar, household, notifications, ai].filter((query) => query.isError)

  return (
    <div className={styles.page}>
      <h1 className={styles.title}>Settings</h1>
      <div className={styles.banner}>
        <span className={styles.avatar} aria-hidden="true">
          K
        </span>
        <p className={styles.bannerText}>
          <strong className={styles.bannerTitle}>Our kitchen</strong>
          <span>Shared household · changes apply to everyone</span>
        </p>
      </div>
      {failed.length > 0 && (
        <div className={styles.alert}>
          <ErrorBanner onRetry={() => failed.forEach((query) => void query.refetch())}>
            Settings could not load: {failed.map((query) => query.error?.message).join('; ')}
          </ErrorBanner>
        </div>
      )}
      <Group label="Household">
        <SettingsRow
          to="/settings/regulars"
          icon={Heart}
          title="Regular dinners"
          status={status(regulars, regularDinnersStatus)}
        />
        <SettingsRow
          to="/settings/calendar"
          icon={Calendar}
          title="Google Calendar"
          status={status(calendar, calendarStatus)}
        />
        <SettingsRow
          to="/settings/household"
          icon={House}
          title="Household"
          status={status(household, (s) => `${s.timezone} · Dinner at ${formatClockTime(s.dinner_time)}`)}
        />
      </Group>
      <Group label="Alerts">
        <SettingsRow
          to="/settings/notifications"
          icon={Bell}
          title="Notifications & Google Chat"
          status={status(notifications, notificationsStatus)}
        />
      </Group>
      <Group label="Admin" admin>
        <SettingsRow
          to="/settings/ai"
          icon={Crosshair}
          title="AI assistant"
          status={status(ai, aiStatus)}
          tag={<Tag className={styles.tag}>Admin</Tag>}
        />
      </Group>
    </div>
  )
}

// status is a row's status line: a skeleton while its query loads, the
// formatted data once it has, and nothing when it failed (the error banner
// says why).
function status<T>(query: UseQueryResult<T>, format: (data: T) => string): ReactNode {
  if (query.isPending) return <Skeleton className={styles.statusSkeleton} />
  if (query.isError) return null
  return format(query.data)
}

type GroupProps = {
  label: string
  // The Admin group's card carries the grape-300 inset ring.
  admin?: boolean
  children: ReactNode
}

function Group({ label, admin = false, children }: GroupProps) {
  const id = useId()
  return (
    <section aria-labelledby={id}>
      <h2 id={id} className={styles.groupLabel}>
        {label}
      </h2>
      <Card className={[styles.group, admin && styles.admin].filter(Boolean).join(' ')}>
        <ul className={styles.rows}>{children}</ul>
      </Card>
    </section>
  )
}

type SettingsRowProps = {
  to: string
  icon: LucideIcon
  title: string
  status?: ReactNode
  tag?: ReactNode
}

function SettingsRow({ to, icon, title, status, tag }: SettingsRowProps) {
  return (
    <li className={styles.item}>
      <Link to={to} className={styles.row}>
        <span className={styles.icon}>
          <Icon icon={icon} />
        </span>
        <span className={styles.body}>
          <span className={styles.rowTitle}>{title}</span>
          {status && <span className={styles.status}>{status}</span>}
        </span>
        {tag}
        <Icon icon={ChevronRight} />
      </Link>
    </li>
  )
}
