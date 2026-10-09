import { useMemo } from 'react'
import { useApi } from '../api/ApiContext.tsx'
import {
  browserPushEnvironment,
  pushStatus,
  subscribePush,
  unsubscribePush,
  type PushStatus,
  type SubscribePushResult,
  type UnsubscribePushResult,
} from './subscribe.ts'

export type Push = {
  // pushStatus reads whether Web Push is on for this device.
  pushStatus(): Promise<PushStatus>
  // subscribePush turns on Web Push on this device; call it only from a tap.
  subscribePush(): Promise<SubscribePushResult>
  // unsubscribePush turns off Web Push on this device.
  unsubscribePush(): Promise<UnsubscribePushResult>
}

// usePush binds pushStatus, subscribePush and unsubscribePush to the app's
// API client and this browser.
export function usePush(): Push {
  const api = useApi()
  return useMemo(
    () => ({
      pushStatus: () => pushStatus(browserPushEnvironment()),
      subscribePush: () => subscribePush(api, browserPushEnvironment()),
      unsubscribePush: () => unsubscribePush(api, browserPushEnvironment()),
    }),
    [api],
  )
}
