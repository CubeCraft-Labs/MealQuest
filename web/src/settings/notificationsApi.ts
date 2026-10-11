import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useApi } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'
import type { PushStatus } from '../push/subscribe.ts'
import { usePush } from '../push/usePush.ts'
import { clockTime, HOUSEHOLD_SETTINGS_KEY, NOTIFICATIONS_KEY } from './api.ts'

export const NOTIFICATIONS_PATH = '/api/v1/settings/notifications'
export const CHAT_WEBHOOK_PATH = '/api/v1/settings/chat-webhook'
export const CHAT_WEBHOOK_TEST_PATH = '/api/v1/settings/chat-webhook/test'

// notificationEvents are the events the household chooses channels for
// (internal/household NotificationEvents), in the order the Notifications
// screen lists them. rotation_review is the next-day Rotation check, "Keep it
// in rotation?".
export const notificationEvents = [
  'plan_ready',
  'list_ready',
  'day_of_reminder',
  'new_suggestions',
  'rotation_review',
] as const

export type NotificationEvent = (typeof notificationEvents)[number]

export type NotificationPref = { event: NotificationEvent; push: boolean; chat: boolean }

// NotificationSettings is GET and the PUT /api/v1/settings/notifications
// body: every event's channels, in notificationEvents order, and when the
// day-of dinner reminder is sent (the household settings' reminder_time,
// 24-hour HH:MM).
export type NotificationSettings = { prefs: NotificationPref[]; reminder_time: string }

// parseNotificationSettings refuses a body that is not every event's
// channels, in order, with an HH:MM reminder time.
function parseNotificationSettings(where: string, body: unknown): NotificationSettings {
  if (!isRecord(body) || !Array.isArray(body.prefs) || typeof body.reminder_time !== 'string') {
    throw new Error(`${where}: response is not the notification settings`)
  }
  if (!clockTime.test(body.reminder_time)) {
    throw new Error(`${where}: reminder_time ${JSON.stringify(body.reminder_time)} is not HH:MM`)
  }
  const prefs: unknown[] = body.prefs
  if (prefs.length !== notificationEvents.length) {
    throw new Error(`${where}: want ${notificationEvents.length} events, got ${prefs.length}`)
  }
  return {
    prefs: notificationEvents.map((event, i) => {
      const pref = prefs[i]
      if (!isRecord(pref) || pref.event !== event || typeof pref.push !== 'boolean' || typeof pref.chat !== 'boolean') {
        throw new Error(`${where}: event ${i} is not ${event}'s channels`)
      }
      return { event, push: pref.push, chat: pref.chat }
    }),
    reminder_time: body.reminder_time,
  }
}

export function useNotificationSettings() {
  const { apiFetch } = useApi()
  return useQuery({
    queryKey: NOTIFICATIONS_KEY,
    queryFn: async () =>
      parseNotificationSettings(`GET ${NOTIFICATIONS_PATH}`, await apiFetch<unknown>(NOTIFICATIONS_PATH)),
  })
}

// useSaveNotificationSettings replaces every event's channels and the
// reminder time. reminder_time is a household setting, so the household
// settings are read again.
export function useSaveNotificationSettings() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (settings: NotificationSettings) =>
      parseNotificationSettings(
        `PUT ${NOTIFICATIONS_PATH}`,
        await apiFetch<unknown>(NOTIFICATIONS_PATH, { method: 'PUT', body: settings }),
      ),
    onSuccess: (settings) => {
      queryClient.setQueryData(NOTIFICATIONS_KEY, settings)
      void queryClient.invalidateQueries({ queryKey: HOUSEHOLD_SETTINGS_KEY })
    },
  })
}

// ChatWebhook is GET /api/v1/settings/chat-webhook. The URL itself is never
// sent: url_masked has no key and only the token's last few characters.
// Every field is null while no webhook is stored.
export type ChatWebhook = {
  space_label: string | null
  url_masked: string | null
  // When a test message was last sent to this webhook and whether Google
  // Chat accepted it; null until one is.
  last_test_at: string | null
  last_test_status: 'ok' | 'failed' | null
}

// ChatWebhookTest is POST /api/v1/settings/chat-webhook/test: the status
// Google Chat answered the test message with, whatever it was.
export type ChatWebhookTest = { http_status: number; settings: ChatWebhook }

function isStringOrNull(value: unknown): value is string | null {
  return value === null || typeof value === 'string'
}

function parseChatWebhook(where: string, body: unknown): ChatWebhook {
  if (
    !isRecord(body) ||
    !isStringOrNull(body.space_label) ||
    !isStringOrNull(body.url_masked) ||
    !isStringOrNull(body.last_test_at) ||
    !(body.last_test_status === null || body.last_test_status === 'ok' || body.last_test_status === 'failed')
  ) {
    throw new Error(`${where}: response is not the Google Chat webhook settings`)
  }
  if ((body.url_masked === null) !== (body.space_label === null)) {
    throw new Error(`${where}: a webhook URL and its space name must be stored together`)
  }
  return {
    space_label: body.space_label,
    url_masked: body.url_masked,
    last_test_at: body.last_test_at,
    last_test_status: body.last_test_status,
  }
}

const CHAT_WEBHOOK_KEY = ['settings', 'chat-webhook'] as const

export function useChatWebhook() {
  const { apiFetch } = useApi()
  return useQuery({
    queryKey: CHAT_WEBHOOK_KEY,
    queryFn: async () => parseChatWebhook(`GET ${CHAT_WEBHOOK_PATH}`, await apiFetch<unknown>(CHAT_WEBHOOK_PATH)),
  })
}

// useSaveChatWebhook stores a webhook URL and its space's name, replacing
// the stored one.
export function useSaveChatWebhook() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (webhook: { url: string; space_label: string }) =>
      parseChatWebhook(
        `PUT ${CHAT_WEBHOOK_PATH}`,
        await apiFetch<unknown>(CHAT_WEBHOOK_PATH, { method: 'PUT', body: webhook }),
      ),
    onSuccess: (webhook) => queryClient.setQueryData(CHAT_WEBHOOK_KEY, webhook),
  })
}

// useTestChatWebhook sends a test message to the stored webhook.
export function useTestChatWebhook() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (): Promise<ChatWebhookTest> => {
      const where = `POST ${CHAT_WEBHOOK_TEST_PATH}`
      const body = await apiFetch<unknown>(CHAT_WEBHOOK_TEST_PATH, { method: 'POST' })
      if (!isRecord(body) || typeof body.http_status !== 'number' || !Number.isInteger(body.http_status)) {
        throw new Error(`${where}: response has no http_status`)
      }
      return { http_status: body.http_status, settings: parseChatWebhook(where, body) }
    },
    onSuccess: (test) => queryClient.setQueryData(CHAT_WEBHOOK_KEY, test.settings),
  })
}

export const PUSH_STATUS_KEY = ['push', 'status'] as const

// usePushStatus is whether Web Push is on for this device. It asks only the
// browser, so it runs offline too.
export function usePushStatus() {
  const push = usePush()
  return useQuery({ queryKey: PUSH_STATUS_KEY, queryFn: () => push.pushStatus(), networkMode: 'always' })
}

// notificationsStatus is the settings home's "Push on · Dinner Crew space"
// line: whether Web Push is on for this device, and the Google Chat space.
export function notificationsStatus(push: PushStatus, chat: ChatWebhook): string {
  return `${push === 'on' ? 'Push on' : 'Push off'} · ${chat.space_label ?? 'Google Chat not connected'}`
}
