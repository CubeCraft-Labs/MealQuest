import { useState } from 'react'
import { Share, X } from 'lucide-react'
import { Card } from '../ui/Card.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { IconButton } from '../ui/IconButton.tsx'
import styles from './InstallHint.module.css'

// DISMISSED_KEY is the localStorage key remembering, on this device, that the
// household dismissed the hint.
export const DISMISSED_KEY = 'mealquest.installHint.dismissed'

// Safari's user agent ends "(KHTML, like Gecko) Version/<v> Mobile/<build>
// Safari/<v>" (iPadOS leaves out Mobile/). Every other iOS browser and in-app
// web view adds its own token (CriOS, FxiOS, EdgiOS, OPiOS, MQQBrowser, GSA,
// ...) or drops Version/ or Safari/, so only this exact tail is Safari.
const SAFARI_TAIL = /\(KHTML, like Gecko\) Version\/[\d.]+ (?:Mobile\/\w+ )?Safari\/[\d.]+$/

// isIosSafari reports whether the page is open in Safari on iPhone, iPod or
// iPad. iPadOS 13+ asks for the desktop site and reports itself as a Mac, so
// a "Macintosh" with a touch screen is an iPad.
export function isIosSafari(nav: Pick<Navigator, 'userAgent' | 'maxTouchPoints'>): boolean {
  const ua = nav.userAgent
  const ios = /iPhone|iPod|iPad/.test(ua) || (/Macintosh/.test(ua) && nav.maxTouchPoints > 1)
  return ios && SAFARI_TAIL.test(ua)
}

// isStandalone reports whether the app is running from the Home Screen.
// Safari sets navigator.standalone; other browsers answer the display-mode
// media query.
export function isStandalone(): boolean {
  const legacy = (navigator as Navigator & { standalone?: boolean }).standalone
  return legacy === true || window.matchMedia('(display-mode: standalone)').matches
}

// HintState is whether the household dismissed the hint on this device, and
// the storage failure, if any, that stopped us reading or saving that.
type HintState = {
  dismissed: boolean
  failure: { action: 'read' | 'write'; message: string } | null
}

// Storage can be unavailable (blocked by the browser or privacy settings), in
// which case using it throws. The failure is reported, never treated as a
// remembered dismissal.
function readDismissed(): HintState {
  try {
    return { dismissed: window.localStorage.getItem(DISMISSED_KEY) === '1', failure: null }
  } catch (err) {
    return { dismissed: false, failure: { action: 'read', message: String(err) } }
  }
}

function writeDismissed(): HintState {
  try {
    window.localStorage.setItem(DISMISSED_KEY, '1')
    return { dismissed: true, failure: null }
  } catch (err) {
    return { dismissed: false, failure: { action: 'write', message: String(err) } }
  }
}

const failureText = {
  read: 'Could not check whether this hint was dismissed on this device',
  write: 'Could not remember on this device that this hint was dismissed',
}

// InstallHint tells iOS and iPadOS Safari users how to install the app, which
// iOS requires before it delivers Web Push. It shows only in a Safari browser
// tab, never once installed, and stays dismissed on this device. If storage
// fails, the hint stays up with an error saying so. Used by onboarding step 3
// and the Notifications screen.
export function InstallHint() {
  const [eligible] = useState(() => isIosSafari(navigator) && !isStandalone())
  const [state, setState] = useState<HintState>(() =>
    eligible ? readDismissed() : { dismissed: false, failure: null },
  )
  if (!eligible || state.dismissed) return null

  const { failure } = state
  return (
    <>
      <Card tone="muted" role="note" className={styles.hint}>
        <Icon icon={Share} className={styles.icon} />
        <p className={styles.message}>Tap Share, then Add to Home Screen to get notifications</p>
        <IconButton label="Dismiss" onClick={() => setState(writeDismissed())}>
          <Icon icon={X} />
        </IconButton>
      </Card>
      {failure && (
        <ErrorBanner onRetry={() => setState(failure.action === 'read' ? readDismissed() : writeDismissed())}>
          {failureText[failure.action]}: {failure.message}
        </ErrorBanner>
      )}
    </>
  )
}
