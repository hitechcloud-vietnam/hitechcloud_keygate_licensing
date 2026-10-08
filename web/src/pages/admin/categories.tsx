import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Pencil, Plus, Trash2 } from "lucide-react"
import { useState } from "react"
import { showToast } from "@/components/toast"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableEmpty,
  DataTableHead,
  DataTableHeader,
  DataTablePagination,
  DataTableRow,
  useServerPagination,
} from "@/components/ui/data-table"
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useI18n } from "@/i18n"
import { admin, type Category, type CategoryInput } from "@/lib/api"

// Admin CRUD for the marketplace catalog's categories. Rows are
// discovery facets only — they shape how the public catalog is
// browsed and never change what a licence may do. The list is
// paginated (data.{categories,total,limit,offset}); create derives a
// slug from the name when none is sent; update is a partial PATCH;
// delete cascades the product links (a product is never deleted, only
// detached from that facet).
export default function CategoriesPage() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [search, setSearch] = useState("")
  const pg = useServerPagination(10, [search])
  const { data, isLoading } = useQuery({
    queryKey: ["admin", "categories", search, pg.page, pg.pageSize],
    queryFn: () => admin.listCategories({ search, ...pg.params }),
  })
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<Category | null>(null)
  const [deleting, setDeleting] = useState<Category | null>(null)

  const { items: categories, total, totalPages } = pg.from(data, data?.categories)

  const deleteMut = useMutation({
    mutationFn: (id: string) => admin.deleteCategory(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "categories"] })
      showToast(t("toast.categoryDeleted"), "success")
      setDeleting(null)
    },
    onError: (e: Error) => showToast(e.message, "error"),
  })

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-2xl font-bold tracking-tight sr-only md:not-sr-only">{t("categories.title")}</h1>
          <p className="text-muted-foreground">{t("categories.subtitle")}</p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus className="h-4 w-4 mr-2" /> {t("categories.new")}
        </Button>
      </div>

      <div className="flex flex-wrap gap-3">
        <Input
          placeholder={t("common.search")}
          value={search}
          onChange={(e) => {
            setSearch(e.target.value)
            pg.setPage(0)
          }}
          className="w-full sm:w-64"
        />
      </div>

      <Card>
        <CardContent className="pt-6">
          {isLoading ? (
            <div className="h-32 animate-pulse bg-muted rounded-lg" />
          ) : (
            <>
              <DataTable>
                <DataTableHeader>
                  <DataTableRow>
                    <DataTableHead>{t("common.name")}</DataTableHead>
                    <DataTableHead>{t("categories.slug")}</DataTableHead>
                    <DataTableHead>{t("categories.description")}</DataTableHead>
                    <DataTableHead>{t("categories.position")}</DataTableHead>
                    <DataTableHead className="w-24 text-right">{t("common.actions")}</DataTableHead>
                  </DataTableRow>
                </DataTableHeader>
                <DataTableBody>
                  {categories.length === 0 && <DataTableEmpty colSpan={5} message={t("categories.empty")} />}
                  {categories.map((c: Category) => (
                    <DataTableRow key={c.id}>
                      <DataTableCell className="font-medium">{c.name}</DataTableCell>
                      <DataTableCell>
                        <code className="text-xs bg-muted px-1.5 py-0.5 rounded">{c.slug}</code>
                      </DataTableCell>
                      <DataTableCell className="max-w-xs truncate text-muted-foreground">
                        {c.description || "—"}
                      </DataTableCell>
                      <DataTableCell className="text-muted-foreground">{c.position}</DataTableCell>
                      <DataTableCell>
                        <div className="flex justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            title={t("common.edit")}
                            aria-label={t("common.edit")}
                            onClick={() => setEditing(c)}
                          >
                            <Pencil className="h-4 w-4" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            title={t("common.delete")}
                            aria-label={t("common.delete")}
                            onClick={() => setDeleting(c)}
                          >
                            <Trash2 className="h-4 w-4 text-destructive" />
                          </Button>
                        </div>
                      </DataTableCell>
                    </DataTableRow>
                  ))}
                </DataTableBody>
              </DataTable>
              {total > 0 && (
                <DataTablePagination
                  page={pg.page}
                  totalPages={totalPages}
                  total={total}
                  pageSize={pg.pageSize}
                  onPageChange={pg.setPage}
                  onPageSizeChange={pg.setPageSize}
                />
              )}
            </>
          )}
        </CardContent>
      </Card>

      {creating && <CategoryDialog open onClose={() => setCreating(false)} />}
      {editing && <CategoryDialog open onClose={() => setEditing(null)} category={editing} />}

      <AlertDialog open={!!deleting} onOpenChange={() => setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("common.delete")} "{deleting?.name}"?
            </AlertDialogTitle>
            <AlertDialogDescription>{t("categories.deleteConfirm")}</AlertDialogDescription>
          </AlertDialogHeader>
          <div className="flex justify-end gap-2">
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={() => deleting && deleteMut.mutate(deleting.id)}
            >
              {t("common.delete")}
            </AlertDialogAction>
          </div>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

// slugify mirrors the server's NormalizeCategorySlug closely enough for
// the create-time preview: lower-case, runs of non [a-z0-9] collapse to
// a single hyphen, no leading/trailing hyphen. The server folds the
// authoritative value, so this is only what the admin sees before save.
function slugify(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/(^-|-$)/g, "")
}

function CategoryDialog({ open, onClose, category }: { open: boolean; onClose: () => void; category?: Category }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [name, setName] = useState(category?.name || "")
  const [slug, setSlug] = useState(category?.slug || "")
  const [description, setDescription] = useState(category?.description || "")
  const [position, setPosition] = useState(category != null ? String(category.position) : "0")

  const saveMut = useMutation({
    mutationFn: (data: CategoryInput) =>
      category ? admin.updateCategory(category.id, data) : admin.createCategory(data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "categories"] })
      showToast(category ? t("toast.categorySaved") : t("toast.categoryCreated"), "success")
      onClose()
    },
    onError: (e: Error) => showToast(e.message, "error"),
  })

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    // Position is an integer the server rejects when negative. Parse the
    // typed digits only — no float math — and clamp to the floor.
    const pos = Number.parseInt(position.trim(), 10)
    saveMut.mutate({
      name,
      slug: slug.trim(),
      description,
      position: Number.isFinite(pos) ? Math.max(0, pos) : 0,
    })
  }

  return (
    <Dialog open={open} onOpenChange={onClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{category ? t("categories.editTitle") : t("categories.createTitle")}</DialogTitle>
          <DialogDescription>{t("categories.formDesc")}</DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="space-y-4">
          <DialogBody className="space-y-4">
            <div className="space-y-2">
              <Label>{t("common.name")}</Label>
              <Input
                value={name}
                onChange={(e) => {
                  setName(e.target.value)
                  // Derive the handle while creating; on edit the stored
                  // slug is authoritative and must not be clobbered.
                  if (!category) setSlug(slugify(e.target.value))
                }}
                required
              />
            </div>
            <div className="space-y-2">
              <Label>{t("categories.slug")}</Label>
              <Input value={slug} onChange={(e) => setSlug(e.target.value)} />
              <p className="text-xs text-muted-foreground">{t("categories.slugHint")}</p>
            </div>
            <div className="space-y-2">
              <Label>{t("categories.description")}</Label>
              <textarea
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                rows={3}
                className="flex w-full rounded-md border border-input bg-background px-3 py-2 text-sm shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
              />
            </div>
            <div className="space-y-2">
              <Label>{t("categories.position")}</Label>
              <Input
                type="number"
                min={0}
                step={1}
                inputMode="numeric"
                value={position}
                onChange={(e) => setPosition(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">{t("categories.positionHint")}</p>
            </div>
          </DialogBody>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={saveMut.isPending}>
              {saveMut.isPending ? t("common.loading") : t("common.save")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
