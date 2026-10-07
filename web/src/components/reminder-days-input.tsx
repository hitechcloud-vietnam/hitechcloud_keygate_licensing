import { X } from "lucide-react"
import { useId, useRef, useState } from "react"
import { HelpTip } from "@/components/help-tip"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useI18n } from "@/i18n"

// Mirrors service.ParseReminderDays: whole days 1–90, at most 5, stored
// largest first as "7,3,1". The server validates again on save.
export const DEFAULT_REMINDER_DAYS = [7, 3, 1]
const MAX_DAY = 90
const MAX_COUNT = 5

// The schedules most installs want, one click each; anything else is the
// custom editor below them.
const PRESETS: number[][] = [DEFAULT_REMINDER_DAYS, [14, 7, 1], [30, 7, 1], [3, 1]]

// Splits typed or pasted input: "7, 3 1", "7,3,1", "7、3、1".
const splitDays = (raw: string) => raw.split(/[\s,，、;；]+/).filter(Boolean)
const isDay = (s: string) => /^\d+$/.test(s) && Number(s) >= 1 && Number(s) <= MAX_DAY
const sameDays = (a: number[], b: number[]) => a.join(",") === b.join(",")

// What the server sends with: the stored list when it is valid, otherwise
// the default — never a partly-read list that differs from what is used.
export function parseReminderDays(raw: string | undefined): number[] {
  const parts = splitDays(raw ?? "")
  if (parts.length === 0 || !parts.every(isDay)) return DEFAULT_REMINDER_DAYS
  const days = [...new Set(parts.map(Number))].sort((a, b) => b - a)
  return days.length <= MAX_COUNT ? days : DEFAULT_REMINDER_DAYS
}

// The expiry reminder schedule, shown under its email switch: one summary
// line, with presets and a custom editor behind "Change" so the switch
// list stays compact.
export function ReminderDaysInput({
  value,
  onChange,
  disabled,
}: {
  value: string | undefined
  onChange: (value: string) => void
  disabled?: boolean
}) {
  const { t, locale } = useI18n()
  const days = parseReminderDays(value)
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState("")
  const [error, setError] = useState("")
  const inputRef = useRef<HTMLInputElement>(null)
  const changeRef = useRef<HTMLButtonElement>(null)
  const panelId = useId()
  const inputId = useId()
  const errorId = useId()

  const dayLabel = (n: number) => (n === 1 ? t("settings.reminderDayOne", { n }) : t("settings.reminderDayMany", { n }))
  const listFormat = new Intl.ListFormat(locale, { style: "long", type: "conjunction" })
  const summary = t("settings.reminderDaysSummary", { list: listFormat.format(days.map(dayLabel)) })
  const commit = (next: number[]) => onChange([...next].sort((a, b) => b - a).join(","))

  const add = () => {
    const parts = splitDays(draft)
    if (parts.length === 0) return
    if (parts.some((p) => !isDay(p))) {
      setError(t("settings.reminderDaysInvalid", { max: MAX_DAY }))
      return
    }
    const fresh = [...new Set(parts.map(Number))].filter((n) => !days.includes(n))
    if (fresh.length === 0) {
      setError(t("settings.reminderDaysDuplicate", { day: dayLabel(Number(parts[0])) }))
      return
    }
    if (days.length + fresh.length > MAX_COUNT) {
      setError(t("settings.reminderDaysMax", { max: MAX_COUNT }))
      return
    }
    commit([...days, ...fresh])
    setDraft("")
    setError("")
  }

  const remove = (d: number) => {
    commit(days.filter((x) => x !== d))
    setError("")
    // The button that had focus is gone; keep the keyboard user in place.
    inputRef.current?.focus()
  }

  const close = () => {
    setOpen(false)
    setDraft("")
    setError("")
    // "Change" mounts again on the next render; focus it then.
    requestAnimationFrame(() => changeRef.current?.focus())
  }

  const expanded = open && !disabled

  return (
    <div className="space-y-2">
      <div
        className={`flex flex-wrap items-center gap-x-2 gap-y-1 text-xs ${disabled ? "text-muted-foreground/60" : "text-muted-foreground"}`}
      >
        <span>{summary}</span>
        {disabled ? (
          <span>· {t("settings.reminderDaysOff")}</span>
        ) : (
          !expanded && (
            <button
              ref={changeRef}
              type="button"
              className="font-medium text-foreground underline-offset-2 hover:underline"
              aria-expanded={false}
              aria-controls={panelId}
              onClick={() => setOpen(true)}
            >
              {t("settings.reminderDaysChange")}
            </button>
          )
        )}
        <HelpTip text={t("settings.reminderDaysRules")} />
      </div>

      {expanded && (
        <fieldset id={panelId} className="space-y-3 rounded-md border p-3">
          <legend className="sr-only">{t("settings.reminderDaysLegend")}</legend>

          <div className="space-y-1.5">
            <p className="text-xs font-medium">{t("settings.reminderDaysPresets")}</p>
            <div className="flex flex-wrap gap-2">
              {PRESETS.map((preset) => {
                const selected = sameDays(days, preset)
                return (
                  <button
                    key={preset.join(",")}
                    type="button"
                    aria-pressed={selected}
                    className={`rounded-full border px-3 py-1 text-xs transition-colors ${
                      selected ? "border-primary bg-primary text-primary-foreground" : "hover:bg-muted"
                    }`}
                    onClick={() => {
                      commit(preset)
                      setError("")
                    }}
                  >
                    {listFormat.format(preset.map(dayLabel))}
                    {sameDays(preset, DEFAULT_REMINDER_DAYS) && ` ${t("settings.reminderDaysDefaultTag")}`}
                  </button>
                )
              })}
            </div>
          </div>

          <div className="space-y-1.5">
            <p className="text-xs font-medium">{t("settings.reminderDaysCustom")}</p>
            <div className="flex flex-wrap items-center gap-2">
              {days.map((d) => (
                <span
                  key={d}
                  className="inline-flex items-center gap-1 rounded-full border bg-muted px-2.5 py-0.5 text-xs"
                >
                  {dayLabel(d)}
                  {/* The list never empties: stopping reminders is the
                      email's own switch, so the last day has no remove. */}
                  {days.length > 1 && (
                    <button
                      type="button"
                      className="rounded-full p-0.5 text-muted-foreground hover:text-foreground"
                      aria-label={`${t("settings.reminderDaysRemove")}: ${dayLabel(d)}`}
                      onClick={() => remove(d)}
                    >
                      <X className="h-3 w-3" />
                    </button>
                  )}
                </span>
              ))}
            </div>
            <div className="flex items-center gap-2">
              <label htmlFor={inputId} className="sr-only">
                {t("settings.reminderDaysInputLabel")}
              </label>
              <Input
                id={inputId}
                ref={inputRef}
                type="text"
                inputMode="numeric"
                autoComplete="off"
                className="h-8 w-28"
                placeholder={t("settings.reminderDaysPlaceholder")}
                value={draft}
                aria-invalid={!!error}
                aria-describedby={error ? errorId : undefined}
                onChange={(e) => {
                  setDraft(e.target.value)
                  setError("")
                }}
                onKeyDown={(e) => {
                  if (e.key === "Enter") {
                    e.preventDefault()
                    add()
                  } else if (e.key === "Escape") {
                    e.preventDefault()
                    close()
                  }
                }}
              />
              <Button type="button" variant="outline" size="sm" disabled={draft.trim() === ""} onClick={add}>
                {t("settings.reminderDaysAdd")}
              </Button>
            </div>
            {error && (
              <p id={errorId} role="alert" className="text-xs text-destructive">
                {error}
              </p>
            )}
          </div>

          <div className="flex justify-end">
            <Button type="button" size="sm" variant="secondary" onClick={close}>
              {t("settings.reminderDaysDone")}
            </Button>
          </div>
        </fieldset>
      )}
    </div>
  )
}
