import { ChevronLeft, Heart, Pencil, Plus } from 'lucide-react'
import { ApiError } from '../api/index.ts'
import { useId, useState, type FormEvent, type ReactNode } from 'react'
import { useNavigate } from 'react-router'
import { useOpenRecipe } from '../dinners/useOpenRecipe.tsx'
import {
  DIFFICULTIES,
  METHODS,
  MEAL_SOURCE_LABEL,
  opensRecipe,
  useAddRegular,
  useEditRegular,
  useGroundRegular,
  useRecipeCandidates,
  useRegulars,
  useRemoveRegular,
  useToggleFavorite,
  type Difficulty,
  type GroundingStatus,
  type ManualRecipe,
  type MealSource,
  type Method,
  type NewRegular,
  type RecipeFix,
  type ListedRegular,
  type Regular,
  type RegularPatch,
} from '../dinners/api.ts'
import { useRefusals } from '../dinners/useRefusals.ts'
import { Button } from '../ui/Button.tsx'
import { Card } from '../ui/Card.tsx'
import { ErrorBanner } from '../ui/ErrorBanner.tsx'
import { Icon } from '../ui/Icon.tsx'
import { IconButton } from '../ui/IconButton.tsx'
import { ListRow } from '../ui/ListRow.tsx'
import { Photo } from '../ui/Photo.tsx'
import { Segmented } from '../ui/Segmented.tsx'
import { Select } from '../ui/Select.tsx'
import { Sheet } from '../ui/Sheet.tsx'
import { Skeleton } from '../ui/Skeleton.tsx'
import { Tag } from '../ui/Tag.tsx'
import { TextInput } from '../ui/TextInput.tsx'
import { usePhotoBackfill, type BackfillResult, type PhotoBackfill } from './photoBackfill.ts'
import styles from './RegularsSettingsPage.module.css'

// The most minutes a dinner may take, as the dinners API allows: a day.
const MAX_MINUTES = 1440

export const methodLabels: Record<Method, string> = {
  stovetop: 'Stovetop',
  oven: 'Oven',
  crockpot: 'Crock pot',
  grill: 'Grill',
  other: 'Other',
}

const difficultyLabels: Record<Difficulty, string> = { easy: 'Easy', medium: 'Medium', hard: 'Hard' }

// A pasted link is added as a recipe link under a name the household gives;
// anything else is a dinner name.
const LINK = /^https?:\/\//i

// Settings · Regular dinners: the household's Regulars, each with its photo,
// method and minutes, favorite heart and whether its recipe was found. The
// field at the top adds one by name, or by pasted recipe link with a name
// the household gives it; tapping one opens its recipe, and its pencil
// edits or removes it, or fixes its recipe when none was found. Find
// missing photos searches again for every dinner left without one.
export function RegularsSettingsPage() {
  const navigate = useNavigate()
  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <IconButton label="Back to Settings" onClick={() => void navigate('/settings')}>
          <Icon icon={ChevronLeft} size="nav" />
        </IconButton>
        <div>
          <p className={styles.kicker}>Settings</p>
          <h1 className={styles.title}>Regular dinners</h1>
        </div>
      </header>
      <p className={styles.shared}>Changes apply to everyone in the household.</p>
      <RegularsManager />
    </div>
  )
}

// Which sheet is open, for which Regular.
type Open = { sheet: 'edit' | 'fix'; id: string } | null

function RegularsManager() {
  const recipe = useOpenRecipe()
  const regulars = useRegulars()
  const add = useAddRegular()
  const remove = useRemoveRegular()
  const favorite = useToggleFavorite()
  const { refusals, track } = useRefusals()
  const backfill = usePhotoBackfill()
  const [draft, setDraft] = useState('')
  const [linkName, setLinkName] = useState('')
  const [open, setOpen] = useState<Open>(null)
  const headingId = useId()
  const linkHintId = useId()
  const isLink = LINK.test(draft.trim())
  const ready = draft.trim() !== '' && (!isLink || linkName.trim() !== '')
  const list = regulars.data ?? []
  // A Regular removed meanwhile closes its sheet, and one whose recipe was
  // found meanwhile closes its fix-recipe sheet.
  const opened = open && list.find((regular) => regular.id === open.id && !regular.pending)
  const fixing = opened && open.sheet === 'fix' && opened.grounding_status === 'failed' ? opened : undefined

  function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!ready) return
    const text = draft.trim()
    const body: NewRegular = isLink ? { name: linkName.trim(), recipe_url: text } : { name: text }
    track(`add:${body.name.toLowerCase()}`, `add ${body.name}`, add.mutateAsync(body))
    setDraft('')
    setLinkName('')
  }

  function onRemove(regular: Regular) {
    setOpen(null)
    track(`remove:${regular.id}`, `remove ${regular.name}`, remove.mutateAsync(regular))
  }

  function onFavorite(regular: Regular) {
    track(`favorite:${regular.id}`, `change the favorite heart on ${regular.name}`, favorite.mutateAsync(regular))
  }

  return (
    <>
      <form className={styles.addForm} onSubmit={onSubmit}>
        <div className={styles.add}>
          <TextInput
            label="Add a Regular"
            labelHidden
            className={styles.addInput}
            placeholder="Dinner name or recipe link"
            autoComplete="off"
            enterKeyHint="done"
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
          />
          <IconButton type="submit" label="Add Regular" tone="accent" disabled={!ready}>
            <Icon icon={Plus} size="nav" />
          </IconButton>
        </div>
        {isLink && (
          <>
            <TextInput
              label="Dinner name"
              placeholder="What do you call this dinner?"
              autoComplete="off"
              enterKeyHint="done"
              aria-describedby={linkHintId}
              value={linkName}
              onChange={(event) => setLinkName(event.target.value)}
            />
            <p id={linkHintId} className={styles.hint}>
              Name it yourself: a link can carry private details, so it never names a dinner.
            </p>
          </>
        )}
      </form>
      {refusals.map((refusal) => (
        <p key={refusal.key} role="alert" className={styles.problem}>
          {refusal.text}
        </p>
      ))}

      {regulars.isPending && <Skeleton className={styles.loading} />}
      {regulars.isError && (
        <ErrorBanner onRetry={() => void regulars.refetch()}>
          Your Regulars could not load: {regulars.error.message}
        </ErrorBanner>
      )}
      {recipe.notice}
      {regulars.isSuccess && (
        <section aria-labelledby={headingId} className={styles.section}>
          <h2 id={headingId} className={styles.groupLabel}>
            On rotation · {list.length}
          </h2>
          {list.length === 0 ? (
            <p className={styles.hint}>No Regulars yet. Add the dinners your household makes on repeat.</p>
          ) : (
            <Card className={styles.card}>
              <ul className={styles.rows} aria-label="Regulars">
                {list.map((regular) => (
                  <RegularRow
                    key={regular.id}
                    regular={regular}
                    onOpen={() => recipe.open(regular)}
                    onEdit={() =>
                      setOpen({
                        sheet: regular.meal_source === 'cook' && regular.grounding_status === 'failed' ? 'fix' : 'edit',
                        id: regular.id,
                      })
                    }
                    onFavorite={() => onFavorite(regular)}
                  />
                ))}
              </ul>
            </Card>
          )}
        </section>
      )}
      {regulars.isSuccess && <FindMissingPhotos backfill={backfill} />}

      {opened && open.sheet === 'edit' && (
        <EditRegularSheet
          key={opened.id}
          regular={opened}
          onClose={() => setOpen(null)}
          onRemove={() => onRemove(opened)}
        />
      )}
      {fixing && (
        <FixRecipeSheet
          key={fixing.id}
          regular={fixing}
          backfill={backfill}
          onClose={() => setOpen(null)}
          onEdit={() => setOpen({ sheet: 'edit', id: fixing.id })}
        />
      )}
    </>
  )
}

// details is a Regular's "method · minutes" line, or null when neither is
// known yet.
export function details(regular: Regular): string | null {
  const parts = [
    regular.method && methodLabels[regular.method],
    regular.minutes_total !== null && `${regular.minutes_total} min`,
  ].filter((part): part is string => typeof part === 'string')
  return parts.length === 0 ? null : parts.join(' · ')
}

const badges: Record<GroundingStatus, { text: string; className: string | undefined }> = {
  pending: { text: 'Finding recipe…', className: styles.finding },
  grounded: { text: 'Recipe found', className: styles.found },
  manual: { text: 'Recipe found', className: styles.found },
  failed: { text: 'Needs a recipe', className: styles.needs },
}

function GroundingBadge({ status }: { status: GroundingStatus }) {
  const badge = badges[status]
  return <Tag className={`${styles.badge} ${badge.className}`}>{badge.text}</Tag>
}

// groundingReasons says why a dinner's recipe was not found, by its
// grounding_error reason code.
const groundingReasons: Record<string, string> = {
  no_recipe_found: 'No recipe page the search found had a recipe we could use.',
  recipe_unusable: 'The pasted recipe link has no recipe we could use.',
  search_not_configured: 'Recipe search is not set up. An admin can set it up in Settings.',
  search_failed: "The recipe search couldn't be reached.",
  ai_failed: "The AI assistant couldn't read the recipe.",
  interrupted: 'The server stopped while finding the recipe.',
  grounding_error: 'Something went wrong on the server.',
}

// groundingReason says why a dinner's recipe was not found; an unknown
// reason code is shown as it is.
export function groundingReason(code: string): string {
  return groundingReasons[code] ?? code
}

// backfillJobFailures says why a photo backfill job failed, by its failure
// code; an unknown code is shown as it is.
const backfillJobFailures: Record<string, string> = {
  internal_error: 'Something went wrong on the server.',
}

// backfillProblem says what went wrong with a photo backfill, or null.
function backfillProblem(backfill: PhotoBackfill): string | null {
  if (backfill.startError) return `Photo search not started: ${backfill.startError.message}`
  if (backfill.status === 'failed' && backfill.error) {
    return `Photo search stopped: ${backfillJobFailures[backfill.error.message] ?? backfill.error.message}`
  }
  if (backfill.error) return `Couldn't check on the photo search: ${backfill.error.message}`
  return null
}

function count(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`
}

// backfillSummary says what a finished photo backfill did.
function backfillSummary(result: BackfillResult): string {
  const tally = (outcome: string) => result.dinners.filter((dinner) => dinner.outcome === outcome).length
  if (result.dinners.length === 0) return 'No dinners were missing a photo.'
  const parts = [count(tally('grounded'), 'photo found', 'photos found')]
  const failed = tally('failed')
  if (failed > 0) parts.push(`${count(failed, 'dinner still needs', 'dinners still need')} a recipe`)
  const skipped = tally('skipped')
  if (skipped > 0) parts.push(`${count(skipped, 'dinner', 'dinners')} changed meanwhile, left as it was`)
  return `Photo search done: ${parts.join(' · ')}.`
}

// FindMissingPhotos searches again, by name, for a recipe and photo for
// every dinner left without a photo because its recipe was not found.
function FindMissingPhotos({ backfill }: { backfill: PhotoBackfill }) {
  const headingId = useId()
  const problem = backfillProblem(backfill)
  return (
    <section aria-labelledby={headingId} className={styles.section}>
      <h2 id={headingId} className={styles.groupLabel}>
        Missing photos
      </h2>
      <Card className={`${styles.card} ${styles.backfill}`}>
        <p className={styles.hint}>
          Search again for a recipe and photo for every dinner whose recipe wasn&apos;t found. Dinners with a pasted
          recipe link keep it.
        </p>
        <Button disabled={backfill.active} onClick={backfill.start}>
          {backfill.active ? 'Finding photos…' : 'Find missing photos'}
        </Button>
        <p role="status" className={styles.status}>
          {backfill.active && 'Finding missing photos… this can take a few minutes.'}
          {backfill.status === 'succeeded' && backfill.result && backfillSummary(backfill.result)}
        </p>
        {problem && (
          <p role="alert" className={styles.problem}>
            {problem}
          </p>
        )}
      </Card>
    </section>
  )
}

type RegularRowProps = {
  regular: ListedRegular
  onOpen: () => void
  onEdit: () => void
  onFavorite: () => void
}

// RegularRow opens the Regular's recipe once the server has it (onOpen,
// useOpenRecipe), unless it is still being added or
// the list is the service worker's cached copy, whose Regular another device
// may have archived since (opensRecipe), when the row is plain; its pencil
// opens the edit sheet, or the fix-recipe sheet when its recipe was not found. The pencil and the
// heart sit apart, so their 44 px hit boxes never overlap.
function RegularRow({ regular, onOpen, onEdit, onFavorite }: RegularRowProps) {
  const cooked = regular.meal_source === 'cook'
  const line = cooked ? details(regular) : null
  const fix = cooked && regular.grounding_status === 'failed'
  return (
    <li className={styles.item} aria-busy={regular.pending}>
      <ListRow
        className={regular.pending && styles.pending}
        leading={
          <Photo
            id={cooked ? regular.photo_id : undefined}
            name={regular.name}
            radius="14px"
            sizes="48px"
            className={styles.photo}
          />
        }
        title={regular.name}
        meta={
          <span className={styles.meta}>
            {line && <span>{line}</span>}
            {cooked ? (
              <GroundingBadge status={regular.grounding_status} />
            ) : (
              <Tag tone="teal">{MEAL_SOURCE_LABEL[regular.meal_source]}</Tag>
            )}
          </span>
        }
        onClick={!regular.pending && opensRecipe('regular', regular.cached, regular.meal_source) ? onOpen : undefined}
        trailing={
          <span className={styles.rowActions}>
            <IconButton
              label={fix ? `Fix recipe for ${regular.name}` : `Edit ${regular.name}`}
              disabled={regular.pending}
              onClick={onEdit}
            >
              <Icon icon={Pencil} size="nav" />
            </IconButton>
            <IconButton
              label={`Favorite ${regular.name}`}
              aria-pressed={regular.is_favorite}
              disabled={regular.pending}
              onClick={onFavorite}
            >
              <Icon icon={Heart} size="nav" className={regular.is_favorite ? styles.filled : undefined} />
            </IconButton>
          </span>
        }
      />
    </li>
  )
}

type SheetFrameProps = {
  title: string
  onClose: () => void
  children: ReactNode
}

function SheetFrame({ title, onClose, children }: SheetFrameProps) {
  const titleId = useId()
  return (
    <Sheet open onClose={onClose} aria-labelledby={titleId}>
      <div className={styles.sheet}>
        <h2 id={titleId} className={styles.sheetTitle}>
          {title}
        </h2>
        {children}
      </div>
    </Sheet>
  )
}

// minutesProblem is why a typed minutes value cannot be saved, or null.
function minutesProblem(text: string): string | null {
  if (text === '') return null
  const minutes = Number(text)
  if (!Number.isInteger(minutes) || minutes < 1 || minutes > MAX_MINUTES) {
    return `Minutes must be a whole number from 1 to ${MAX_MINUTES}.`
  }
  return null
}

type Draft = { name: string; method: Method | ''; minutes: string; difficulty: Difficulty | ''; meal_source: MealSource }

const mealSourceOptions = (['cook', 'delivery', 'dining_out'] as const).map((value) => ({
  value,
  label: MEAL_SOURCE_LABEL[value],
}))

function draftOf(regular: Regular): Draft {
  return {
    name: regular.name,
    method: regular.method ?? '',
    minutes: regular.minutes_total === null ? '' : String(regular.minutes_total),
    difficulty: regular.difficulty ?? '',
    meal_source: regular.meal_source,
  }
}

// patchOf is the PATCH body for the fields draft changes. A field the
// Regular has no value for stays unset until one is chosen.
function patchOf(regular: Regular, draft: Draft): RegularPatch {
  const saved = draftOf(regular)
  const patch: RegularPatch = {}
  if (draft.name.trim() !== saved.name) patch.name = draft.name.trim()
  if (draft.method !== saved.method && draft.method !== '') patch.method = draft.method
  if (draft.minutes !== saved.minutes && draft.minutes !== '') patch.minutes_total = Number(draft.minutes)
  if (draft.difficulty !== saved.difficulty && draft.difficulty !== '') patch.difficulty = draft.difficulty
  if (draft.meal_source !== saved.meal_source) patch.meal_source = draft.meal_source
  return patch
}

type EditRegularSheetProps = {
  regular: Regular
  onClose: () => void
  onRemove: () => void
}

// EditRegularSheet renames a Regular and sets its method, minutes and
// difficulty, or removes it from the rotation once confirmed.
function EditRegularSheet({ regular, onClose, onRemove }: EditRegularSheetProps) {
  const [draft, setDraft] = useState(() => draftOf(regular))
  const [confirming, setConfirming] = useState(false)
  const edit = useEditRegular()
  const patch = patchOf(regular, draft)
  const minutesError = minutesProblem(draft.minutes)
  const nameError = draft.name.trim() === '' ? 'A Regular needs a name.' : null
  const canSave = Object.keys(patch).length > 0 && minutesError === null && nameError === null

  function change(next: Partial<Draft>) {
    edit.reset()
    setDraft((current) => ({ ...current, ...next }))
  }

  function onSave(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!canSave) return
    edit.mutate({ id: regular.id, patch }, { onSuccess: onClose })
  }

  if (confirming) {
    return (
      <SheetFrame title={`Remove ${regular.name}?`} onClose={onClose}>
        <p className={styles.hint}>
          It leaves your Regulars, so suggestions and plans stop using it. You can add it again later.
        </p>
        <div className={styles.actions}>
          <Button onClick={onRemove}>Remove</Button>
          <Button variant="outline" onClick={() => setConfirming(false)}>
            Keep it
          </Button>
        </div>
      </SheetFrame>
    )
  }

  return (
    <SheetFrame title={`Edit ${regular.name}`} onClose={onClose}>
      <form className={styles.form} onSubmit={onSave}>
        <fieldset className={styles.lock} disabled={edit.isPending}>
          <Segmented
            label="How this dinner happens"
            options={mealSourceOptions}
            value={draft.meal_source}
            onValueChange={(meal_source) => change({ meal_source })}
          />
          <TextInput
            label="Name"
            value={draft.name}
            autoComplete="off"
            aria-invalid={nameError !== null}
            onChange={(event) => change({ name: event.target.value })}
          />
          {nameError && <p className={styles.problem}>{nameError}</p>}
          <div className={styles.columns}>
            <Select label="Method" value={draft.method} onChange={(event) => change({ method: event.target.value as Method })}>
              {regular.method === null && (
                <option value="" disabled>
                  Not set
                </option>
              )}
              {METHODS.map((method) => (
                <option key={method} value={method}>
                  {methodLabels[method]}
                </option>
              ))}
            </Select>
            <TextInput
              label="Minutes"
              className={styles.paired}
              type="number"
              inputMode="numeric"
              min={1}
              max={MAX_MINUTES}
              value={draft.minutes}
              aria-invalid={minutesError !== null}
              onChange={(event) => change({ minutes: event.target.value })}
            />
          </div>
          {minutesError && <p className={styles.problem}>{minutesError}</p>}
          <Select
            label="Difficulty"
            value={draft.difficulty}
            onChange={(event) => change({ difficulty: event.target.value as Difficulty })}
          >
            {regular.difficulty === null && (
              <option value="" disabled>
                Not set
              </option>
            )}
            {DIFFICULTIES.map((difficulty) => (
              <option key={difficulty} value={difficulty}>
                {difficultyLabels[difficulty]}
              </option>
            ))}
          </Select>
          {edit.isError && (
            <p role="alert" className={styles.problem}>
              {edit.error instanceof ApiError && edit.error.code === 'planned'
                ? `Remove ${regular.name} from the plan first.`
                : `Not saved: ${edit.error.message}`}
            </p>
          )}
          <div className={styles.actions}>
            <Button type="submit" disabled={!canSave}>
              {edit.isPending ? 'Saving…' : 'Save changes'}
            </Button>
            <Button variant="outline" onClick={() => setConfirming(true)}>
              Remove from Regulars
            </Button>
          </div>
        </fieldset>
      </form>
    </SheetFrame>
  )
}

type FixPath = 'pick' | 'link' | 'manual'

const fixPaths = [
  { value: 'pick', label: 'Pick one' },
  { value: 'link', label: 'Paste a link' },
  { value: 'manual', label: 'Type it in' },
] as const

type FixRecipeSheetProps = {
  regular: Regular
  backfill: PhotoBackfill
  onClose: () => void
  onEdit: () => void
}

// lines is text split into its non-blank lines, trimmed.
function lines(text: string): string[] {
  return text
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line !== '')
}

// retryOutcomes says what searching again did for this dinner.
const retryOutcomes = {
  grounded: 'Searched again: a recipe and photo were found.',
  failed: 'Searched again: still no recipe found.',
  skipped: 'It changed while it was searched again, so it was left as it is.',
} as const

// FixRecipeSheet shows why a Regular's recipe was not found and grounds it:
// by searching its name again (a photo backfill), in one of up to three
// recipe pages found for its name, in a pasted recipe link, or in
// ingredients and steps typed in. Searching again is offered only for a
// dinner the backfill searches (photo_backfill_eligible): it never replaces
// a pasted link.
function FixRecipeSheet({ regular, backfill, onClose, onEdit }: FixRecipeSheetProps) {
  const [path, setPath] = useState<FixPath>('pick')
  const ground = useGroundRegular()

  function fix(body: RecipeFix) {
    ground.mutate({ id: regular.id, fix: body }, { onSuccess: onClose })
  }

  return (
    <SheetFrame title="Fix recipe" onClose={onClose}>
      <p className={styles.hint}>
        We couldn&apos;t find a recipe for {regular.name}. Pick a match below, paste a recipe link or type it in.
      </p>
      {regular.grounding_error !== null && (
        <p className={styles.reason}>
          <span className={styles.reasonLabel}>Why: </span>
          {groundingReason(regular.grounding_error)}
        </p>
      )}
      {regular.photo_backfill_eligible && (
        <RetrySearch id={regular.id} backfill={backfill} disabled={ground.isPending} />
      )}
      <Segmented
        label="How to fix the recipe"
        options={fixPaths}
        value={path}
        onValueChange={(next) => {
          ground.reset()
          setPath(next)
        }}
      />
      <fieldset className={styles.lock} disabled={ground.isPending}>
        {path === 'pick' && <PickCandidate id={regular.id} onPick={(candidate_id) => fix({ candidate_id })} />}
        {path === 'link' && <PasteLink onSubmit={(recipe_url) => fix({ recipe_url })} />}
        {path === 'manual' && <ManualEntry onSubmit={(manual) => fix({ manual })} />}
      </fieldset>
      <p role="status" className={styles.status}>
        {ground.isPending && 'Finding recipe… this can take a minute.'}
      </p>
      {ground.isError && (
        <p role="alert" className={styles.problem}>
          Not fixed: {ground.error.message}
        </p>
      )}
      <Button variant="outline" disabled={ground.isPending} onClick={onEdit}>
        Edit or remove {regular.name}
      </Button>
    </SheetFrame>
  )
}

// RetrySearch searches the dinner's name again with a photo backfill, and
// says what it did for the dinner once it ends. The backfill searches
// every dinner without a photo, this one with them.
function RetrySearch({ id, backfill, disabled }: { id: string; backfill: PhotoBackfill; disabled: boolean }) {
  const problem = backfillProblem(backfill)
  const tried = backfill.result?.dinners.find((dinner) => dinner.dinner_id === id)
  let status: string | null = null
  if (backfill.active) status = 'Searching again… this can take a few minutes.'
  else if (backfill.status === 'succeeded') {
    status = tried ? retryOutcomes[tried.outcome] : 'This dinner was not included in that search.'
  }
  return (
    <>
      <Button variant="outline" disabled={disabled || backfill.active} onClick={backfill.start}>
        {backfill.active ? 'Searching again…' : 'Search again'}
      </Button>
      <p role="status" className={styles.status}>
        {status}
      </p>
      {problem && (
        <p role="alert" className={styles.problem}>
          {problem}
        </p>
      )}
    </>
  )
}

function PickCandidate({ id, onPick }: { id: string; onPick: (candidateId: string) => void }) {
  const candidates = useRecipeCandidates(id)
  if (candidates.isPending) return <Skeleton className={styles.candidatesLoading} />
  if (candidates.isError) {
    return (
      <ErrorBanner onRetry={() => void candidates.refetch()}>
        Recipe matches could not load: {candidates.error.message}
      </ErrorBanner>
    )
  }
  if (candidates.data.length === 0) {
    return <p className={styles.hint}>No matches were found. Paste a recipe link or type it in.</p>
  }
  return (
    <ul className={styles.candidates} aria-label="Recipe matches">
      {candidates.data.map((candidate) => (
        <li key={candidate.candidate_id}>
          <button
            type="button"
            className={styles.candidate}
            aria-label={`Use ${candidate.title} from ${candidate.site_name}`}
            onClick={() => onPick(candidate.candidate_id)}
          >
            <Photo id={candidate.photo_id} name={candidate.title} radius="14px" sizes="56px" className={styles.candidatePhoto} />
            <span className={styles.candidateBody}>
              <span className={styles.candidateTitle}>{candidate.title}</span>
              <span className={styles.candidateSite}>{candidate.site_name}</span>
            </span>
          </button>
        </li>
      ))}
    </ul>
  )
}

function PasteLink({ onSubmit }: { onSubmit: (recipeUrl: string) => void }) {
  const [link, setLink] = useState('')
  const url = link.trim()
  return (
    <form
      className={styles.form}
      onSubmit={(event) => {
        event.preventDefault()
        if (url !== '') onSubmit(url)
      }}
    >
      <TextInput
        label="Recipe link"
        type="url"
        inputMode="url"
        placeholder="https://"
        autoComplete="off"
        value={link}
        onChange={(event) => setLink(event.target.value)}
      />
      <Button type="submit" disabled={url === ''}>
        Use this recipe
      </Button>
    </form>
  )
}

function ManualEntry({ onSubmit }: { onSubmit: (manual: ManualRecipe) => void }) {
  const [ingredients, setIngredients] = useState('')
  const [steps, setSteps] = useState('')
  const [minutes, setMinutes] = useState('')
  const [method, setMethod] = useState<Method | ''>('')
  const ingredientsId = useId()
  const stepsId = useId()
  const minutesError = minutesProblem(minutes)
  const ingredientLines = lines(ingredients)
  const missing = method === '' || minutes === ''
  const canSave = ingredientLines.length > 0 && !missing && minutesError === null

  function onSave(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!canSave) return
    onSubmit({ ingredients: ingredientLines, steps: lines(steps), minutes: Number(minutes), method })
  }

  return (
    <form className={styles.form} onSubmit={onSave}>
      <div className={styles.field}>
        <label htmlFor={ingredientsId} className={styles.label}>
          Ingredients, one per line
        </label>
        <textarea
          id={ingredientsId}
          className={styles.textarea}
          rows={5}
          value={ingredients}
          onChange={(event) => setIngredients(event.target.value)}
        />
      </div>
      <div className={styles.field}>
        <label htmlFor={stepsId} className={styles.label}>
          Steps, one per line
        </label>
        <textarea
          id={stepsId}
          className={styles.textarea}
          rows={5}
          value={steps}
          onChange={(event) => setSteps(event.target.value)}
        />
      </div>
      <div className={styles.columns}>
        <Select label="Method" value={method} onChange={(event) => setMethod(event.target.value as Method | '')}>
          <option value="">Not set</option>
          {METHODS.map((m) => (
            <option key={m} value={m}>
              {methodLabels[m]}
            </option>
          ))}
        </Select>
        <TextInput
          label="Minutes"
          className={styles.paired}
          type="number"
          inputMode="numeric"
          min={1}
          max={MAX_MINUTES}
          value={minutes}
          aria-invalid={minutesError !== null}
          onChange={(event) => setMinutes(event.target.value)}
        />
      </div>
      {minutesError && <p className={styles.problem}>{minutesError}</p>}
      {ingredientLines.length > 0 && missing && <p className={styles.problem}>Choose a method and minutes to save.</p>}
      <Button type="submit" disabled={!canSave}>
        Save recipe
      </Button>
    </form>
  )
}
