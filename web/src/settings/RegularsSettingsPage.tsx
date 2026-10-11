import { ChevronLeft, Heart, Plus } from 'lucide-react'
import { useId, useState, type FormEvent, type ReactNode } from 'react'
import { useNavigate } from 'react-router'
import {
  DIFFICULTIES,
  METHODS,
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
  type Method,
  type NewRegular,
  type RecipeFix,
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
// the household gives it; tapping one
// edits or removes it, or fixes its recipe when none was found.
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

// RegularsManager lists, adds, edits and fixes the recipes of the
// household's Regulars: this page's body, and the Settings "Regular
// dinners" sub-tab at tablet and desktop widths.
export function RegularsManager() {
  const regulars = useRegulars()
  const add = useAddRegular()
  const remove = useRemoveRegular()
  const favorite = useToggleFavorite()
  const { refusals, track } = useRefusals()
  const [draft, setDraft] = useState('')
  const [linkName, setLinkName] = useState('')
  const [open, setOpen] = useState<Open>(null)
  const headingId = useId()
  const linkHintId = useId()
  const isLink = LINK.test(draft.trim())
  const ready = draft.trim() !== '' && (!isLink || linkName.trim() !== '')
  const list = regulars.data ?? []
  // A Regular removed meanwhile closes its sheet.
  const opened = open && list.find((regular) => regular.id === open.id && !regular.pending)

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
                    onOpen={() =>
                      setOpen({ sheet: regular.grounding_status === 'failed' ? 'fix' : 'edit', id: regular.id })
                    }
                    onFavorite={() => onFavorite(regular)}
                  />
                ))}
              </ul>
            </Card>
          )}
        </section>
      )}

      {opened && open.sheet === 'edit' && (
        <EditRegularSheet
          key={opened.id}
          regular={opened}
          onClose={() => setOpen(null)}
          onRemove={() => onRemove(opened)}
        />
      )}
      {opened && open.sheet === 'fix' && (
        <FixRecipeSheet
          key={opened.id}
          regular={opened}
          onClose={() => setOpen(null)}
          onEdit={() => setOpen({ sheet: 'edit', id: opened.id })}
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

type RegularRowProps = {
  regular: Regular
  onOpen: () => void
  onFavorite: () => void
}

function RegularRow({ regular, onOpen, onFavorite }: RegularRowProps) {
  const line = details(regular)
  return (
    <li className={styles.item} aria-busy={regular.pending}>
      <ListRow
        className={regular.pending && styles.pending}
        leading={<Photo id={regular.photo_id} name={regular.name} radius="14px" sizes="48px" className={styles.photo} />}
        title={regular.name}
        meta={
          <span className={styles.meta}>
            {line && <span>{line}</span>}
            <GroundingBadge status={regular.grounding_status} />
          </span>
        }
        onClick={regular.pending ? undefined : onOpen}
        trailing={
          <IconButton
            label={`Favorite ${regular.name}`}
            aria-pressed={regular.is_favorite}
            disabled={regular.pending}
            onClick={onFavorite}
          >
            <Icon icon={Heart} size="nav" className={regular.is_favorite ? styles.filled : undefined} />
          </IconButton>
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

type Draft = { name: string; method: Method | ''; minutes: string; difficulty: Difficulty | '' }

function draftOf(regular: Regular): Draft {
  return {
    name: regular.name,
    method: regular.method ?? '',
    minutes: regular.minutes_total === null ? '' : String(regular.minutes_total),
    difficulty: regular.difficulty ?? '',
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
              Not saved: {edit.error.message}
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

// FixRecipeSheet grounds a Regular whose recipe was not found: in one of
// up to three recipe pages found for its name, in a pasted recipe link, or
// in ingredients and steps typed in.
function FixRecipeSheet({ regular, onClose, onEdit }: FixRecipeSheetProps) {
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
            aria-label={candidate.site_name === null ? `Use ${candidate.title}` : `Use ${candidate.title} from ${candidate.site_name}`}
            onClick={() => onPick(candidate.candidate_id)}
          >
            <Photo id={candidate.photo_id} name={candidate.title} radius="14px" sizes="56px" className={styles.candidatePhoto} />
            <span className={styles.candidateBody}>
              <span className={styles.candidateTitle}>{candidate.title}</span>
              {candidate.site_name !== null && <span className={styles.candidateSite}>{candidate.site_name}</span>}
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
