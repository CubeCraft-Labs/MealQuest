import { useId, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { useBreakpoint, useOrientation } from '../shell/useBreakpoint.ts'
import { AIAssistantCard } from './AISettingsPage.tsx'
import { HouseholdSettingsSections } from './HouseholdSettingsPage.tsx'
import { LeaveGuard } from './LeaveGuard.tsx'
import { ChatWebhookCard, NotificationsCard } from './NotificationsPage.tsx'
import { RecipeSearchCard } from './RecipeSearchCard.tsx'
import { RegularsManager } from './RegularsSettingsPage.tsx'
import styles from './SettingsTabs.module.css'

type Tab = 'household' | 'regulars' | 'notifications-ai'

const tabs: readonly { value: Tab; label: string }[] = [
  { value: 'household', label: 'Household' },
  { value: 'regulars', label: 'Regular dinners' },
  { value: 'notifications-ai', label: 'Notifications & AI' },
]

const step: Record<string, number> = { ArrowRight: 1, ArrowLeft: -1 }

// Settings at tablet and desktop widths (handoff mock 2e): one page with
// sub-tabs instead of the mobile list. Every tab stays mounted while hidden,
// so a draft survives switching tabs, and one leave guard covers them all.
// hidden hides the page but keeps it mounted, drafts and leave guard included.
// On an upright tablet the two columns stack into one.
export function SettingsTabs({ hidden = false }: { hidden?: boolean }) {
  const layout = useBreakpoint()
  const orientation = useOrientation()
  const [tab, setTab] = useState<Tab>('notifications-ai')
  const [householdDirty, setHouseholdDirty] = useState(false)
  const [aiDirty, setAIDirty] = useState(false)
  const [recipeSearchDirty, setRecipeSearchDirty] = useState(false)
  const [chatDirty, setChatDirty] = useState(false)
  const refs = useRef<(HTMLButtonElement | null)[]>([])
  const id = useId()
  const tabId = (value: Tab) => `${id}-tab-${value}`
  const panelId = (value: Tab) => `${id}-panel-${value}`

  // Arrow keys move between the tabs, wrapping at the ends; Home and End
  // jump to the first and last. Moving selects the tab.
  function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const selected = tabs.findIndex((t) => t.value === tab)
    const delta = step[event.key]
    let next: number
    if (delta !== undefined) next = (selected + delta + tabs.length) % tabs.length
    else if (event.key === 'Home') next = 0
    else if (event.key === 'End') next = tabs.length - 1
    else return
    const nextTab = tabs[next]
    if (!nextTab) throw new Error(`Settings tabs: no tab at index ${next}`)
    event.preventDefault()
    setTab(nextTab.value)
    refs.current[next]?.focus()
  }

  function panel(value: Tab, className: string | undefined, children: ReactNode) {
    return (
      <div
        id={panelId(value)}
        role="tabpanel"
        aria-labelledby={tabId(value)}
        hidden={tab !== value}
        className={className}
      >
        {children}
      </div>
    )
  }

  return (
    <div
      className={[styles.page, layout === 'tablet' && orientation === 'portrait' && styles.portrait].filter(Boolean).join(' ')}
      hidden={hidden}
    >
      <header className={styles.header}>
        <div className={styles.heading}>
          <h1 className={styles.title}>Settings</h1>
          <p className={styles.shared}>Changes apply to everyone in the household.</p>
        </div>
        <div role="tablist" aria-label="Settings sections" className={styles.tabs} onKeyDown={onKeyDown}>
          {tabs.map(({ value, label }, index) => (
            <button
              key={value}
              ref={(el) => {
                refs.current[index] = el
              }}
              id={tabId(value)}
              type="button"
              role="tab"
              aria-selected={tab === value}
              aria-controls={panelId(value)}
              tabIndex={tab === value ? 0 : -1}
              className={tab === value ? `${styles.tab} ${styles.selected}` : styles.tab}
              onClick={() => setTab(value)}
            >
              {label}
            </button>
          ))}
        </div>
      </header>

      {panel('household', styles.single, <HouseholdSettingsSections onDirtyChange={setHouseholdDirty} />)}

      {panel('regulars', styles.single, <RegularsManager />)}

      {panel(
        'notifications-ai',
        styles.columns,
        <>
          <div className={styles.column}>
            <NotificationsCard />
            <ChatWebhookCard onDirtyChange={setChatDirty} />
          </div>
          <div className={styles.column}>
            <AIAssistantCard onDirtyChange={setAIDirty} />
            <RecipeSearchCard onDirtyChange={setRecipeSearchDirty} />
          </div>
        </>,
      )}

      <LeaveGuard dirty={householdDirty || aiDirty || recipeSearchDirty || chatDirty} />
    </div>
  )
}
