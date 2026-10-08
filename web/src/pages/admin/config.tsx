import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { RotateCcw, Save } from "lucide-react"
import { useEffect, useRef, useState } from "react"
import { showToast, toastError } from "@/components/toast"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { useI18n } from "@/i18n"
import type { ConfigEntry } from "@/lib/api"
import { admin } from "@/lib/api"

// Admin Config UI (config-in-DB). The catalog comes from
// GET /admin/config — categories of typed keys with human
// descriptions — and every save sends ONLY the keys that changed:
// plain values under `values`, secret material under `secrets`.
//
// Secrets are write-only: the API never returns one (`value` is
// always empty, `set` says whether one is stored), so the editor is
// "leave blank to keep" and the set/unset badge is the only feedback
// a stored secret gives. Resetting a key (DELETE) drops the stored
// value back to the default — or to its environment fallback.
//
// Environment variables are FALLBACK-only: a value saved here wins.
// That note is on the page because it is the question every operator
// asks here first.
export default function AdminConfigPage() {
  const { t } = useI18n()
  const qc = useQueryClient()

  const catalog = useQuery({
    queryKey: ["admin", "config"],
    queryFn: () => admin.getConfig(),
  })

  // The edit buffer, keyed by config key. Values keep their wire
  // strings ("true"/"false" for bools) so "changed" is a string
  // comparison and a typed-back value can never drift.
  const [form, setForm] = useState<Record<string, string>>({})
  const [secrets, setSecrets] = useState<Record<string, string>>({})
  const [confirmReset, setConfirmReset] = useState<string | null>(null)
  // baseline is what the server last said; dirty tracks whether the
  // operator has edited anything since — so a background refetch
  // (most of all the one a save triggers) re-seeds only a clean form.
  const baseline = useRef<Record<string, string>>({})
  const dirty = useRef(false)

  useEffect(() => {
    const cats = catalog.data?.categories
    if (!cats) return
    const next: Record<string, string> = {}
    for (const cat of cats) for (const key of cat.keys) next[key.key] = key.value
    baseline.current = next
    if (!dirty.current) {
      setForm(next)
      setSecrets({})
    }
  }, [catalog.data])

  const changedValues = () => {
    const out: Record<string, string> = {}
    for (const [k, v] of Object.entries(form)) {
      if (baseline.current[k] !== undefined && baseline.current[k] !== v) out[k] = v
    }
    return out
  }
  const changedSecrets = () => {
    const out: Record<string, string> = {}
    for (const [k, v] of Object.entries(secrets)) if (v !== "") out[k] = v
    return out
  }
  const dirtyCount = Object.keys(changedValues()).length + Object.keys(changedSecrets()).length

  const saveMut = useMutation({
    mutationFn: () => admin.updateConfig({ values: changedValues(), secrets: changedSecrets() }),
    onSuccess: (res) => {
      dirty.current = false
      qc.invalidateQueries({ queryKey: ["admin", "config"] })
      showToast(t("config.saved"), "success")
      if (res.restarted_required?.length) {
        showToast(t("config.restartNeeded", { keys: res.restarted_required.join(", ") }), "success")
      }
    },
    onError: (e: Error) => toastError(e),
  })

  const resetMut = useMutation({
    mutationFn: (key: string) => admin.resetConfigKey(key),
    onSuccess: (_res, key) => {
      setConfirmReset(null)
      dirty.current = false
      qc.invalidateQueries({ queryKey: ["admin", "config"] })
      showToast(t("config.resetDone", { key }), "success")
    },
    onError: (e: Error) => {
      setConfirmReset(null)
      toastError(e)
    },
  })

  const categories = catalog.data?.categories || []

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">{t("config.title")}</h1>
          <p className="text-muted-foreground">{t("config.subtitle")}</p>
          <p className="mt-1 text-xs text-muted-foreground">{t("config.envNote")}</p>
        </div>
        <Button onClick={() => saveMut.mutate()} disabled={saveMut.isPending || dirtyCount === 0}>
          <Save className="h-4 w-4 mr-2" />
          {saveMut.isPending
            ? t("common.loading")
            : dirtyCount > 0
              ? t("config.saveChanges", { count: dirtyCount })
              : t("common.save")}
        </Button>
      </div>

      {catalog.isLoading && <div className="h-64 animate-pulse bg-muted rounded-lg" />}
      {catalog.isError && (
        <Card>
          <CardContent className="py-10 text-center text-muted-foreground">{t("config.loadError")}</CardContent>
        </Card>
      )}

      {categories.length > 0 && (
        <Tabs defaultValue={categories[0].id}>
          {/* Tabs wrap rather than scroll (ui/tabs) — a category bar
              that runs off a narrow screen hides its last entries. */}
          <TabsList>
            {categories.map((cat) => (
              <TabsTrigger key={cat.id} value={cat.id}>
                {cat.name}
              </TabsTrigger>
            ))}
          </TabsList>
          {categories.map((cat) => (
            <TabsContent key={cat.id} value={cat.id}>
              <Card>
                <CardHeader>
                  <CardTitle className="text-base">{cat.name}</CardTitle>
                  <CardDescription>{t("config.categoryHint")}</CardDescription>
                </CardHeader>
                <CardContent className="space-y-4">
                  {cat.keys.length === 0 && <p className="text-sm text-muted-foreground">{t("config.noKeys")}</p>}
                  {cat.keys.map((entry) => (
                    <ConfigRow
                      key={entry.key}
                      entry={entry}
                      value={form[entry.key] ?? ""}
                      secret={secrets[entry.key] ?? ""}
                      onValue={(v) => {
                        dirty.current = true
                        setForm((f) => ({ ...f, [entry.key]: v }))
                      }}
                      onSecret={(v) => {
                        dirty.current = true
                        setSecrets((s) => ({ ...s, [entry.key]: v }))
                      }}
                      confirmingReset={confirmReset === entry.key}
                      onResetClick={() => setConfirmReset(confirmReset === entry.key ? null : entry.key)}
                      onResetConfirm={() => resetMut.mutate(entry.key)}
                      resetPending={resetMut.isPending && resetMut.variables === entry.key}
                    />
                  ))}
                </CardContent>
              </Card>
            </TabsContent>
          ))}
        </Tabs>
      )}
    </div>
  )
}

function ConfigRow({
  entry,
  value,
  secret,
  onValue,
  onSecret,
  confirmingReset,
  onResetClick,
  onResetConfirm,
  resetPending,
}: {
  entry: ConfigEntry
  value: string
  secret: string
  onValue: (v: string) => void
  onSecret: (v: string) => void
  confirmingReset: boolean
  onResetClick: () => void
  onResetConfirm: () => void
  resetPending: boolean
}) {
  const { t } = useI18n()
  const inputId = `config-${entry.key}`

  return (
    <div className="space-y-2 rounded-lg border p-4">
      <div className="flex flex-wrap items-center gap-2">
        <Label htmlFor={inputId} className="font-mono text-sm font-semibold">
          {entry.key}
        </Label>
        {entry.restart_required && <Badge variant="outline">{t("config.restartRequired")}</Badge>}
        {entry.secret && (
          <Badge variant={entry.set ? "secondary" : "outline"}>
            {entry.set ? t("config.secretSet") : t("config.secretUnset")}
          </Badge>
        )}
        <span className="ml-auto flex items-center gap-2">
          {confirmingReset ? (
            <>
              <span className="text-xs text-muted-foreground">{t("config.resetConfirm", { key: entry.key })}</span>
              <Button variant="outline" size="sm" onClick={onResetConfirm} disabled={resetPending}>
                {resetPending ? t("common.loading") : t("common.confirm")}
              </Button>
              <Button variant="ghost" size="sm" onClick={onResetClick}>
                {t("common.cancel")}
              </Button>
            </>
          ) : (
            <Button variant="ghost" size="sm" className="text-muted-foreground" onClick={onResetClick}>
              <RotateCcw className="h-3.5 w-3.5 mr-1" /> {t("config.reset")}
            </Button>
          )}
        </span>
      </div>

      {entry.description && <p className="text-sm text-muted-foreground">{entry.description}</p>}

      {entry.secret ? (
        <div className="space-y-1">
          {/* Write-only: the API never returns a secret, so this input
              starts empty and an empty save leaves the stored one
              alone (settings.tsx's rule, kept). */}
          <Input
            id={inputId}
            type="password"
            autoComplete="off"
            value={secret}
            onChange={(e) => onSecret(e.target.value)}
            placeholder={entry.set ? t("config.secretPlaceholder") : t("config.secretUnset")}
          />
        </div>
      ) : entry.type === "bool" ? (
        <label className="flex items-center gap-2 text-sm">
          <input
            id={inputId}
            type="checkbox"
            checked={value === "true"}
            onChange={(e) => onValue(e.target.checked ? "true" : "false")}
            className="h-4 w-4 accent-primary"
          />
          {t("config.enabled")}
        </label>
      ) : (
        <Input
          id={inputId}
          type="text"
          inputMode={entry.type === "int" ? "numeric" : undefined}
          value={value}
          onChange={(e) => onValue(e.target.value)}
          placeholder={entry.type === "duration" ? t("config.durationHint") : undefined}
        />
      )}

      <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
        {entry.default !== "" && <span>{t("config.defaultLabel", { value: entry.default })}</span>}
        {entry.env_var && <span className="font-mono">{t("config.envVarLabel", { name: entry.env_var })}</span>}
      </div>
    </div>
  )
}
