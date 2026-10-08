import { useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { ApiError, useApi } from '../api/index.ts'
import { isRecord } from '../api/errors.ts'

export const GOOGLE_PATH = '/api/v1/integrations/google'
// GOOGLE_CONNECT_PATH starts connecting: the browser opens it as a page,
// and the server sends it on to Google's consent screen and, once Google
// answers, back to /settings/calendar.
export const GOOGLE_CONNECT_PATH = `${GOOGLE_PATH}/connect`
export const GOOGLE_CALENDARS_PATH = `${GOOGLE_PATH}/calendars`
export const GOOGLE_CALENDAR_PATH = `${GOOGLE_PATH}/calendar`

// GOOGLE_RECONNECT_REQUIRED is the API error code of a call Google refused
// the stored grant for: only connecting again helps.
export const GOOGLE_RECONNECT_REQUIRED = 'google_reconnect_required'

export const GOOGLE_STATUSES = ['not_connected', 'connected', 'reconnect_required'] as const

// GoogleStatus is reconnect_required once Google has refused the stored
// grant: the connection and its chosen calendar are kept, but nothing can be
// pushed until the household connects again.
export type GoogleStatus = (typeof GOOGLE_STATUSES)[number]

// GoogleConnection is GET /api/v1/integrations/google: the connection's
// status, the calendar chosen to push dinners to, when it was connected and
// why it needs a reconnect, each null when there is none.
export type GoogleConnection = {
  status: GoogleStatus
  calendar_id: string | null
  calendar_name: string | null
  connected_at: string | null
  last_sync_error: string | null
}

// GoogleCalendar is a calendar MealQuest can push dinners to: one the
// connected account can write to.
export type GoogleCalendar = { id: string; summary: string; primary: boolean }

export const GOOGLE_KEY = ['integrations', 'google'] as const
const GOOGLE_CALENDARS_KEY = ['integrations', 'google', 'calendars'] as const

function isStringOrNull(value: unknown): value is string | null {
  return value === null || typeof value === 'string'
}

function parseConnection(where: string, body: unknown): GoogleConnection {
  if (
    !isRecord(body) ||
    !GOOGLE_STATUSES.includes(body.status as GoogleStatus) ||
    !isStringOrNull(body.calendar_id) ||
    !isStringOrNull(body.calendar_name) ||
    !isStringOrNull(body.connected_at) ||
    !isStringOrNull(body.last_sync_error)
  ) {
    throw new Error(`${where}: response is not a Google Calendar connection`)
  }
  return {
    status: body.status as GoogleStatus,
    calendar_id: body.calendar_id,
    calendar_name: body.calendar_name,
    connected_at: body.connected_at,
    last_sync_error: body.last_sync_error,
  }
}

// reconnectRequired says whether error is Google refusing the stored grant.
export function reconnectRequired(error: Error): boolean {
  return error instanceof ApiError && error.code === GOOGLE_RECONNECT_REQUIRED
}

// noteGoogleFailure reads the connection again after a call Google refused
// the grant for: the server now reports it as reconnect_required.
export function noteGoogleFailure(queryClient: QueryClient, error: Error) {
  if (reconnectRequired(error)) void queryClient.invalidateQueries({ queryKey: GOOGLE_KEY, exact: true })
}

// useGoogleConnection is the Google Calendar connection.
export function useGoogleConnection() {
  const { apiFetch } = useApi()
  return useQuery({
    queryKey: GOOGLE_KEY,
    queryFn: async () => parseConnection(`GET ${GOOGLE_PATH}`, await apiFetch<unknown>(GOOGLE_PATH)),
  })
}

// useGoogleCalendars lists the connected account's calendars MealQuest can
// push to, in Google's order. It asks Google, so it is read only while
// connected.
export function useGoogleCalendars(enabled: boolean) {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useQuery({
    queryKey: GOOGLE_CALENDARS_KEY,
    enabled,
    queryFn: async (): Promise<GoogleCalendar[]> => {
      let body: unknown
      try {
        body = await apiFetch<unknown>(GOOGLE_CALENDARS_PATH)
      } catch (error) {
        noteGoogleFailure(queryClient, error as Error)
        throw error
      }
      if (
        !Array.isArray(body) ||
        !body.every((c) => isRecord(c) && typeof c.id === 'string' && typeof c.summary === 'string' && typeof c.primary === 'boolean')
      ) {
        throw new Error(`GET ${GOOGLE_CALENDARS_PATH}: response is not a calendar list`)
      }
      return body.map((c: Record<string, unknown>) => ({ id: c.id as string, summary: c.summary as string, primary: c.primary as boolean }))
    },
  })
}

// useChooseGoogleCalendar chooses the calendar dinners are pushed to.
// Choosing another calendar than the one pushed to makes every pushed plan
// changed (the next push writes the whole week to the new calendar), so the
// plans are read again.
export function useChooseGoogleCalendar() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (calendarId: string) => {
      const where = `PUT ${GOOGLE_CALENDAR_PATH}`
      const body = await apiFetch<unknown>(GOOGLE_CALENDAR_PATH, { method: 'PUT', body: { calendar_id: calendarId } })
      if (!isRecord(body) || body.calendar_id !== calendarId || typeof body.calendar_name !== 'string') {
        throw new Error(`${where}: response is not the calendar chosen`)
      }
      return { calendar_id: calendarId, calendar_name: body.calendar_name }
    },
    onSuccess: (chosen) => {
      queryClient.setQueryData<GoogleConnection>(GOOGLE_KEY, (connection) => connection && { ...connection, ...chosen })
      void queryClient.invalidateQueries({ queryKey: ['plans'] })
    },
    onError: (error) => noteGoogleFailure(queryClient, error),
  })
}

// useDisconnectGoogle revokes MealQuest's access at Google and forgets the
// connection and every pushed event, making each pushed plan changed: the
// connection and the plans are read again.
export function useDisconnectGoogle() {
  const { apiFetch } = useApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => apiFetch<void>(GOOGLE_PATH, { method: 'DELETE' }),
    onSuccess: () => {
      queryClient.removeQueries({ queryKey: GOOGLE_CALENDARS_KEY })
      void queryClient.invalidateQueries({ queryKey: GOOGLE_KEY, exact: true })
      void queryClient.invalidateQueries({ queryKey: ['plans'] })
    },
  })
}

// calendarStatus is the settings list's line for the connection: the
// handoff's "Connected · Dinners calendar".
export function calendarStatus(connection: GoogleConnection): string {
  switch (connection.status) {
    case 'not_connected':
      return 'Not connected'
    case 'reconnect_required':
      return 'Reconnect needed'
    case 'connected':
      return connection.calendar_name === null ? 'Connected · no calendar chosen' : `Connected · ${connection.calendar_name}`
  }
}
