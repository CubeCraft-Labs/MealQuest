import { useQueryClient } from '@tanstack/react-query'
import { ChevronLeft } from 'lucide-react'
import { Fragment, useEffect, useId, useState } from 'react'
import { useNavigate } from 'react-router'
import { InstallHint } from '../pwa/InstallHint.tsx'
import type { PushStatus, SubscribePushResult, UnsubscribePushResult } from '../push/subscribe.ts'
import { usePush } from '../push/usePush.ts'
import { Button } from '../ui/Button.tsx'
import { Card } from '../ui/Card.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { IconButton } from '../ui/IconButton.tsx'
import { Sheet } from '../ui/Sheet.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import { TextInput } from '../ui/TextInput.tsx'
import { Toggle } from '../ui/Toggle.tsx'
import { clockTime, formatClockTime } from './api.ts'
import { LeaveGuard } from './LeaveGuard.tsx'
import {
  PUSH_STATUS_KEY,
  useChatWebhook,
  useNotificationSettings,
  usePushStatus,
  useSaveChatWebhook,
  useSaveNotificationSettings,
  useTestChatWebhook,
  type ChatWebhook,
  type NotificationEvent,
  type NotificationSettings,
} from './notificationsApi.ts'
import styles from './NotificationsPage.module.css'

// eventLabels names each event as the Notifications screen lists it.
// rotation_review is the next-day Rotation check.
const eventLabels: Record<NotificationEvent, string> = {
  plan_ready: 'New weekly plan ready',
  list_ready: 'Shopping list ready',
  day_of_reminder: 'Day-of dinner reminder',
  new_suggestions: 'New suggestions to swipe',
  rotation_review: 'Keep it in rotation?',
}

// Notifications & Google Chat (handoff mock 1n): which events go by push
// and to Google Chat, when the day-of reminder is sent, Web Push on this
// device, and the household's Google Chat webhook. Each change is saved as
// it is made; only a webhook being replaced is a draft.
export function NotificationsPage() {
  const [dirty, setDirty] = useState(false)
  return (
    <div className={styles.page}>
      <Header />
      <NotificationPrefs />
      <ChatWebhookCard onDirtyChange={setDirty} />
      <ChatPreview />
      <LeaveGuard dirty={dirty} />
    </div>
  )
}

function Header() {
  const navigate = useNavigate()
  return (
    <header className={styles.header}>
      <IconButton label="Back to Settings" onClick={() => void navigate('/settings')}>
        <Icon icon={ChevronLeft} size="nav" />
      </IconButton>
      <h1 className={styles.title}>Notifications</h1>
    </header>
  )
}

// NotificationsCard is the notification preferences as a card, for the
// desktop settings (handoff mock 2e).
export function NotificationsCard() {
  const titleId = useId()
  return (
    <Card className={styles.card} aria-labelledby={titleId} role="region">
      <h2 id={titleId} className={styles.cardTitle}>
        Notifications
      </h2>
      <NotificationPrefs />
    </Card>
  )
}

// NotificationPrefs is the event × channel matrix once it loads, then Web
// Push on this device.
function NotificationPrefs() {
  const settings = useNotificationSettings()
  return (
    <>
      {settings.isPending && <Skeleton className={styles.loading} />}
      {settings.isError && (
        <ErrorBanner onRetry={() => void settings.refetch()}>
          Notification settings could not load: {settings.error.message}
        </ErrorBanner>
      )}
      {settings.isSuccess && <NotificationMatrix saved={settings.data} />}
      <PushPermission />
      <InstallHint />
    </>
  )
}

type Channel = 'push' | 'chat'

const channelNames: Record<Channel, string> = { push: 'Push', chat: 'Google Chat' }

// NotificationMatrix saves every change at once. While a save is pending it
// shows the settings being saved and takes no other change; a failed save
// shows the stored settings again and says why.
function NotificationMatrix({ saved }: { saved: NotificationSettings }) {
  const save = useSaveNotificationSettings()
  const [editingTime, setEditingTime] = useState(false)
  const shown = save.isPending ? save.variables : saved
  const id = useId()
  const reminder = formatClockTime(shown.reminder_time)

  function toggle(event: NotificationEvent, channel: Channel, on: boolean) {
    save.mutate({ ...shown, prefs: shown.prefs.map((p) => (p.event === event ? { ...p, [channel]: on } : p)) })
  }

  return (
    <>
      <div className={styles.matrix} role="group" aria-label="Notification channels">
        <span className={styles.head}>Event</span>
        <span className={styles.head} aria-hidden="true">
          Push
        </span>
        <span className={styles.head} aria-hidden="true">
          Chat
        </span>
        {shown.prefs.map(({ event, push, chat }) => (
          <Fragment key={event}>
            <div className={styles.event}>
              <span id={`${id}-${event}`} className={styles.eventName}>
                {eventLabels[event]}
              </span>
              {event === 'day_of_reminder' && (
                <button
                  type="button"
                  className={styles.time}
                  aria-label={`Reminder time, ${reminder}. Change`}
                  disabled={save.isPending}
                  onClick={() => {
                    save.reset()
                    setEditingTime(true)
                  }}
                >
                  at {reminder}
                </button>
              )}
            </div>
            {(['push', 'chat'] as const).map((channel) => (
              <Toggle
                key={channel}
                className={styles.toggle}
                aria-label={`${eventLabels[event]}: ${channelNames[channel]}`}
                checked={channel === 'push' ? push : chat}
                disabled={save.isPending}
                onCheckedChange={(on) => toggle(event, channel, on)}
              />
            ))}
          </Fragment>
        ))}
      </div>
      {save.isError && !editingTime && (
        <ErrorBanner onRetry={() => save.mutate(save.variables)}>
          Notification settings were not saved: {save.error.message}
        </ErrorBanner>
      )}
      <ReminderTimeSheet
        open={editingTime}
        saved={saved}
        busy={save.isPending}
        error={save.isError ? save.error.message : null}
        onClose={() => {
          save.reset()
          setEditingTime(false)
        }}
        onSave={(reminder_time) =>
          save.mutate({ ...saved, reminder_time }, { onSuccess: () => setEditingTime(false) })
        }
      />
    </>
  )
}

type ReminderTimeSheetProps = {
  open: boolean
  saved: NotificationSettings
  busy: boolean
  // Why the last save failed, or null.
  error: string | null
  onClose: () => void
  onSave: (reminderTime: string) => void
}

// ReminderTimeSheet edits when the day-of dinner reminder is sent.
function ReminderTimeSheet({ open, saved, busy, error, onClose, onSave }: ReminderTimeSheetProps) {
  const titleId = useId()
  const [time, setTime] = useState(saved.reminder_time)
  useEffect(() => {
    if (open) setTime(saved.reminder_time)
  }, [open, saved.reminder_time])
  const valid = clockTime.test(time)

  return (
    <Sheet open={open} onClose={onClose} aria-labelledby={titleId}>
      <div className={styles.sheet}>
        <h2 id={titleId} className={styles.sheetTitle}>
          Day-of dinner reminder
        </h2>
        <p className={styles.sheetText}>When the reminder is sent on each planned day. This changes it for everyone in the household.</p>
        <TextInput
          label="Reminder time"
          type="time"
          value={time}
          required
          onChange={(event) => setTime(event.target.value)}
        />
        {!valid && <p className={styles.note}>Choose a time.</p>}
        {error !== null && (
          <p className={styles.error} role="alert">
            The reminder time was not saved: {error}
          </p>
        )}
        <div className={styles.sheetActions}>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={busy || !valid || time === saved.reminder_time} onClick={() => onSave(time)}>
            {busy ? 'Saving…' : 'Save'}
          </Button>
        </div>
      </div>
    </Sheet>
  )
}

const pushStatusText: Record<PushStatus, string> = {
  on: 'On',
  off: 'Off',
  denied: 'Blocked',
  unsupported: 'Not supported',
}

type PushOutcome = SubscribePushResult | UnsubscribePushResult

// PushPermission is whether Web Push is on for this device, with a button
// to turn it on or off. Turning on asks for notification permission, which
// browsers allow only from a tap, so subscribePush is called straight from
// the button's click.
function PushPermission() {
  const status = usePushStatus()
  const push = usePush()
  const queryClient = useQueryClient()
  const [busy, setBusy] = useState(false)
  const [outcome, setOutcome] = useState<PushOutcome | null>(null)
  const [failure, setFailure] = useState<{ action: 'on' | 'off'; message: string } | null>(null)
  const labelId = useId()

  function run(action: 'on' | 'off', pending: Promise<PushOutcome>) {
    setBusy(true)
    setOutcome(null)
    setFailure(null)
    void pending
      .then(setOutcome, (error: unknown) =>
        setFailure({ action, message: error instanceof Error ? error.message : String(error) }),
      )
      .finally(() => {
        setBusy(false)
        void queryClient.invalidateQueries({ queryKey: PUSH_STATUS_KEY })
      })
  }

  return (
    <div className={styles.permission}>
      <div className={styles.permissionRow} role="group" aria-labelledby={labelId}>
        <p className={styles.permissionText}>
          <span id={labelId}>Notifications on this device</span>
          {': '}
          {status.isPending && <Skeleton className={styles.statusSkeleton} />}
          {status.isSuccess && <strong>{pushStatusText[status.data]}</strong>}
        </p>
        {status.data === 'off' && (
          <Button variant="outline" disabled={busy} onClick={() => run('on', push.subscribePush())}>
            {busy ? 'Turning on…' : 'Turn on'}
          </Button>
        )}
        {status.data === 'on' && (
          <Button variant="outline" disabled={busy} onClick={() => run('off', push.unsubscribePush())}>
            {busy ? 'Turning off…' : 'Turn off'}
          </Button>
        )}
      </div>
      {status.data === 'denied' && (
        <p className={styles.note}>This browser blocks notifications from MealQuest. Allow them in the browser's site settings.</p>
      )}
      {status.data === 'unsupported' && (
        <p className={styles.note}>This browser can't receive notifications.</p>
      )}
      {outcome === 'dismissed' && (
        <p className={styles.note} role="status">
          The permission prompt was closed without an answer, so notifications are still off.
        </p>
      )}
      {status.isError && (
        <ErrorBanner onRetry={() => void status.refetch()}>
          Could not check notifications on this device: {status.error.message}
        </ErrorBanner>
      )}
      {failure !== null && (
        <ErrorBanner>
          Notifications could not be turned {failure.action}: {failure.message}
        </ErrorBanner>
      )}
    </div>
  )
}

type ChatWebhookCardProps = {
  // Called with whether a webhook being entered is unsaved.
  onDirtyChange: (dirty: boolean) => void
}

// ChatWebhookCard is the household's Google Chat webhook: its masked URL,
// the space it posts to, and a test message.
export function ChatWebhookCard({ onDirtyChange }: ChatWebhookCardProps) {
  const webhook = useChatWebhook()
  const titleId = useId()
  return (
    <Card className={styles.card} aria-labelledby={titleId} role="region">
      <h2 id={titleId} className={styles.cardTitle}>
        Google Chat webhook
      </h2>
      {webhook.isPending && <Skeleton className={styles.cardLoading} />}
      {webhook.isError && (
        <ErrorBanner onRetry={() => void webhook.refetch()}>
          The Google Chat webhook could not load: {webhook.error.message}
        </ErrorBanner>
      )}
      {webhook.isSuccess && <ChatWebhookForm saved={webhook.data} onDirtyChange={onDirtyChange} />}
    </Card>
  )
}

function ChatWebhookForm({ saved, onDirtyChange }: ChatWebhookCardProps & { saved: ChatWebhook }) {
  const connected = saved.url_masked !== null
  const [replacing, setReplacing] = useState(!connected)
  const [url, setUrl] = useState('')
  const [space, setSpace] = useState('')
  const save = useSaveChatWebhook()
  const test = useTestChatWebhook()
  const dirty = replacing && (url !== '' || space !== '')

  useEffect(() => {
    onDirtyChange(dirty)
  }, [dirty, onDirtyChange])
  useEffect(() => () => onDirtyChange(false), [onDirtyChange])

  function close() {
    save.reset()
    setReplacing(false)
    setUrl('')
    setSpace('')
  }

  if (replacing) {
    return (
      <fieldset className={styles.lock} disabled={save.isPending}>
        <TextInput
          label="Webhook URL"
          type="url"
          className={`${styles.input} ${styles.mono}`}
          value={url}
          placeholder="https://chat.googleapis.com/v1/spaces/…"
          autoComplete="off"
          spellCheck={false}
          onChange={(event) => setUrl(event.target.value)}
        />
        <TextInput
          label="Space name"
          className={styles.input}
          value={space}
          placeholder="Dinner Crew"
          autoComplete="off"
          onChange={(event) => setSpace(event.target.value)}
        />
        {save.isError && (
          <p className={styles.error} role="alert">
            The webhook was not saved: {save.error.message}
          </p>
        )}
        <div className={styles.webhookActions}>
          {connected && (
            <Button variant="outline" onClick={close}>
              Cancel
            </Button>
          )}
          <Button
            disabled={url.trim() === '' || space.trim() === '' || save.isPending}
            onClick={() => {
              test.reset()
              save.mutate({ url: url.trim(), space_label: space.trim() }, { onSuccess: close })
            }}
          >
            {save.isPending ? 'Saving…' : 'Save webhook'}
          </Button>
        </div>
      </fieldset>
    )
  }

  return (
    <>
      <TextInput
        label="Webhook URL"
        labelHidden
        className={`${styles.input} ${styles.mono}`}
        value={saved.url_masked ?? ''}
        readOnly
      />
      <TestResult test={test} />
      <div className={styles.webhookRow}>
        <p className={styles.connected}>Connected · {saved.space_label}</p>
        <Button variant="outline" disabled={test.isPending} onClick={() => test.mutate()}>
          {test.isPending ? 'Sending…' : 'Send test'}
        </Button>
      </div>
      <button
        type="button"
        className={styles.link}
        onClick={() => {
          test.reset()
          setReplacing(true)
        }}
      >
        Replace webhook
      </button>
    </>
  )
}

// TestResult says how the last Send test ended, under the URL: sent, the
// HTTP status Google Chat refused it with, or why it was not sent.
function TestResult({ test }: { test: ReturnType<typeof useTestChatWebhook> }) {
  if (test.isSuccess) {
    const status = test.data.http_status
    const ok = status >= 200 && status < 300
    return (
      <p className={ok ? styles.testOk : styles.error} role="status">
        {ok ? 'Test message sent.' : `Google Chat refused the test message: HTTP ${status}.`}
      </p>
    )
  }
  if (test.isError) {
    return (
      <p className={styles.error} role="alert">
        Test message not sent: {test.error.message}
      </p>
    )
  }
  return null
}

// ChatPreview shows what a Google Chat message from MealQuest looks like.
function ChatPreview() {
  return (
    <figure className={styles.preview} aria-label="Google Chat message preview">
      <span className={styles.avatar} aria-hidden="true">
        Q
      </span>
      <p className={styles.previewText}>
        <strong>MealQuest</strong> · preview
        <br />
        This week's plan is ready: Tacos, Bolognese, Tikka masala + 4 more. Open the plan →
      </p>
    </figure>
  )
}
