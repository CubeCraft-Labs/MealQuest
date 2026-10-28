import { Bell, ChevronRight, Compass, Search, ShoppingCart, Zap, type LucideIcon } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { opensRecipe } from '../dinners/api.ts'
import { useOpenRecipe, type RecipeDinner } from '../dinners/useOpenRecipe.tsx'
import { RECIPE_NAME_PARAM, RECIPE_PARAM } from '../push/notification.ts'
import { duration } from '../plan/PlanPage.tsx'
import { supplyRunScreen } from '../shopping/api.ts'
import buttonStyles from '../ui/Button.module.css'
import { EmptyState } from '../ui/EmptyState.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { IconButton } from '../ui/IconButton.tsx'
import { Photo } from '../ui/Photo.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import { useHome, type Home, type Origin, type Tonight } from './api.ts'
import styles from './HomePage.module.css'
import { RotationCheck } from './RotationCheck.tsx'
import { UpdatesSheet } from './UpdatesSheet.tsx'

const ORIGIN_LABEL: Record<Origin, string> = { regular: 'Regular', side_quest: 'Side quest' }

// meta is the Tonight card's "Regular · 40 min" line, the time only when
// the recipe says it.
function meta({ origin, minutes_total }: Tonight): string {
  return minutes_total === null ? ORIGIN_LABEL[origin] : `${ORIGIN_LABEL[origin]} · ${duration(minutes_total)}`
}

// TonightCard is the dinner planned for today: its washed photo, name and
// meta line, and the dark Recipe pill, which opens its recipe once the
// server has it (onRecipe, useOpenRecipe), unless the dinner was
// archived (removed from Regulars) after the plan was made — it has no
// detail to open — or Home is the service worker's cached copy (cached),
// whose status may be stale (opensRecipe), when there is no pill.
function TonightCard({ tonight, cached, onRecipe }: { tonight: Tonight; cached: boolean; onRecipe: () => void }) {
  return (
    <section aria-label="Tonight" className={styles.card}>
      <Photo
        id={tonight.meal_source === 'cook' ? tonight.photo_id : undefined}
        name={tonight.name}
        inset="10px"
        sizes="400px"
        className={styles.photo}
      />
      <div className={styles.cardFoot}>
        <div className={styles.cardText}>
          <h2 className={styles.name}>{tonight.name}</h2>
          <span className={styles.meta}>{meta(tonight)}</span>
        </div>
        {opensRecipe(tonight.status, cached, tonight.meal_source) && (
          <button type="button" className={[buttonStyles.button, buttonStyles.dark, styles.recipe].join(' ')} onClick={onRecipe}>
            Recipe
          </button>
        )}
      </div>
    </section>
  )
}

// TonightSlot is the Tonight card for home, or NoPlan when today has no plan.
function ManualTonightCard({ label }: { label: string }) {
  return (
    <section aria-label="Tonight" className={styles.card}>
      <div className={styles.manualFoot}>
        <h2 className={styles.name}>{label}</h2>
      </div>
    </section>
  )
}

function TonightSlot({ home, onRecipe }: { home: Home; onRecipe: (dinner: Tonight) => void }) {
  const { tonight, manualTonight, cached } = home
  if (manualTonight !== null) return <ManualTonightCard label={manualTonight} />
  return tonight === null ? <NoPlan /> : <TonightCard tonight={tonight} cached={cached} onRecipe={() => onRecipe(tonight)} />
}

// NoPlan stands in for the Tonight card when today has no plan.
function NoPlan() {
  const navigate = useNavigate()
  return (
    <div className={styles.card}>
      <EmptyState title="No quest planned tonight" action={{ label: 'Plan this week', onClick: () => void navigate('/plan') }}>
        Draft this week's dinners and tonight's quest shows up here.
      </EmptyState>
    </div>
  )
}

// StatusRow is one neutral row under the tiles: a grape-700 icon, its line
// and a chevron, linking to the screen it is about.
function StatusRow({ to, icon, children }: { to: string; icon: LucideIcon; children: string }) {
  return (
    <li>
      <Link to={to} className={styles.status}>
        <Icon icon={icon} className={styles.statusIcon} />
        <span className={styles.statusText}>{children}</span>
        <Icon icon={ChevronRight} />
      </Link>
    </li>
  )
}

// StatusRows are the rows that apply: Side quests waiting in the deck, and
// a Supply run ready. With neither, there are no rows.
function StatusRows({ home }: { home: Home }) {
  const count = home.sideQuestCount
  const list = home.shoppingList
  if (count === 0 && !list.ready) return null
  return (
    <ul aria-label="Status" className={styles.statuses}>
      {count > 0 && (
        <StatusRow to="/discover" icon={Compass}>
          {`${count} new side ${count === 1 ? 'quest' : 'quests'} to swipe`}
        </StatusRow>
      )}
      {list.ready && (
        <StatusRow to={supplyRunScreen(list.weekStart)} icon={ShoppingCart}>
          {`Shopping list ready · ${list.dates}`}
        </StatusRow>
      )}
    </ul>
  )
}

// Tiles are the two shortcuts, Quick dinner and Cook from what I have.
function Tiles() {
  return (
    <div className={styles.tiles}>
      <Link to="/quick" className={[styles.tile, styles.quick].join(' ')}>
        <Icon icon={Zap} size="nav" />
        <span className={styles.tileLabel}>Quick dinner</span>
      </Link>
      <Link to="/cook-from-pantry" className={[styles.tile, styles.pantry].join(' ')}>
        <Icon icon={Search} size="nav" />
        <span className={styles.tileLabel}>Cook from what I have</span>
      </Link>
    </div>
  )
}

// useNotificationRecipe opens, through open (useOpenRecipe), the recipe a
// tapped notification asked Home to open (recipeOpenPath, from
// src/push/notification.ts), once, and takes the ask out of the address so
// going back to Home does not ask again. It reports whether the ask named no
// dinner, when nothing opens.
function useNotificationRecipe(open: (dinner: RecipeDinner) => void): boolean {
  const [params, setParams] = useSearchParams()
  const [broken, setBroken] = useState(false)
  const id = params.get(RECIPE_PARAM)
  const name = params.get(RECIPE_NAME_PARAM)
  useEffect(() => {
    if (id === null && name === null) return
    setParams({}, { replace: true })
    if (id === null || id === '' || name === null || name.trim() === '') {
      setBroken(true)
      return
    }
    setBroken(false)
    open({ id, name })
  }, [id, name, open, setParams])
  return broken
}

// HomePage is mock 1g, Home: today's date over "Tonight's quest" and the
// Recent updates bell, the Tonight card (or the way to plan the week), the
// Quick dinner and Cook from what I have tiles, and the status rows, all
// from GET /api/v1/home. Above the tiles is the Rotation check, "Keep it in
// rotation?", while a tried dinner waits for the household's answer.
//
// The bell carries a grape dot while there are unread updates and opens the
// Recent updates sheet, which marks them seen.
export function HomePage() {
  const home = useHome()
  const data = home.data
  const [updatesOpen, setUpdatesOpen] = useState(false)
  const recipe = useOpenRecipe()
  const brokenLink = useNotificationRecipe(recipe.open)
  return (
    <div className={styles.page} aria-busy={home.isPending}>
      <header className={styles.header}>
        <div>
          {data !== undefined ? (
            <p className={styles.date}>{data.dateLabel}</p>
          ) : (
            home.isPending && <Skeleton data-testid="skeleton-date" className={styles.skeletonDate} />
          )}
          <h1 className={styles.title}>Tonight's quest</h1>
        </div>
        <IconButton
          label={data?.unread ? 'Recent updates, unread' : 'Recent updates'}
          className={styles.bell}
          onClick={() => setUpdatesOpen(true)}
        >
          <Icon icon={Bell} className={styles.bellIcon} />
          {data?.unread && <span data-testid="unread-dot" className={styles.dot} />}
        </IconButton>
      </header>
      {home.isPending && <Skeleton data-testid="skeleton-tonight" className={styles.skeletonCard} />}
      {home.isError && (
        <ErrorBanner onRetry={() => void home.refetch()}>Couldn't load tonight's quest. {home.error.message}</ErrorBanner>
      )}
      {data !== undefined && <TonightSlot home={data} onRecipe={recipe.open} />}
      {recipe.notice}
      {brokenLink && <ErrorBanner>That notification's recipe link names no dinner, so nothing opened.</ErrorBanner>}
      <RotationCheck />
      <Tiles />
      {home.isPending && (
        <div className={styles.statuses} aria-hidden="true">
          <Skeleton data-testid="skeleton-status" className={styles.skeletonStatus} />
          <Skeleton data-testid="skeleton-status" className={styles.skeletonStatus} />
        </div>
      )}
      {data !== undefined && <StatusRows home={data} />}
      <UpdatesSheet open={updatesOpen} onClose={() => setUpdatesOpen(false)} />
    </div>
  )
}
