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
import { aiProviderLabels, formatClockTime, useAISettings, useHouseholdSettings } from './api.ts'
import styles from './SettingsPage.module.css'

// Settings home (handoff mock 1p). Each row opens its settings page.
//
// Regular dinners, Google Calendar and Notifications & Google Chat show no
// status line yet: the server has no dinners, Google Calendar or
// notification endpoint to read one from.
export function SettingsPage() {
  const household = useHouseholdSettings()
  const ai = useAISettings()
  const failed = [household, ai].filter((query) => query.isError)

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
        <SettingsRow to="/settings/regulars" icon={Heart} title="Regular dinners" />
        <SettingsRow to="/settings/calendar" icon={Calendar} title="Google Calendar" />
        <SettingsRow
          to="/settings/household"
          icon={House}
          title="Household"
          status={status(household, (s) => `${s.timezone} · Dinner at ${formatClockTime(s.dinner_time)}`)}
        />
      </Group>
      <Group label="Alerts">
        <SettingsRow to="/settings/notifications" icon={Bell} title="Notifications & Google Chat" />
      </Group>
      <Group label="Admin" admin>
        <SettingsRow
          to="/settings/ai"
          icon={Crosshair}
          title="AI assistant"
          status={status(ai, (s) => `${aiProviderLabels[s.provider]} · ${s.model}`)}
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
