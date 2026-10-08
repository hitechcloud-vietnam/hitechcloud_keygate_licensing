// RBAC — roles list (plan §8). The custom-role layer that sits ON TOP
// of the built-in roles: named permission bundles and the admin UI to
// manage them. Built-in bundles (is_system) carry a badge and refuse
// delete with 409 SYSTEM_ROLE_PROTECTED; a role still held by users
// refuses delete with 409 ROLE_IN_USE — both surfaced as curated
// toasts, never a silent failure (§90).
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Plus, ShieldCheck, Trash2 } from "lucide-react"
import { useState } from "react"
import { Link } from "react-router-dom"
import { ListEmptyState } from "@/components/empty-state"
import { PermissionPicker } from "@/components/permission-picker"
import { showToast, toastError } from "@/components/toast"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import {
  DataTable,
  DataTableBody,
  DataTableCell,
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
import { ApiError, admin, type Role } from "@/lib/api"
import { formatDate } from "@/lib/utils"

export default function RBACRolesPage() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [search, setSearch] = useState("")
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<Role | null>(null)
  const pg = useServerPagination(10, [search])
  const { data, isLoading } = useQuery({
    queryKey: ["admin", "rbac", "roles", search, pg.page, pg.pageSize],
    queryFn: () => admin.listRoles({ search: search || undefined, ...pg.params }),
  })
  const { items: roles, total, totalPages } = pg.from(data, data?.roles)

  const deleteMut = useMutation({
    mutationFn: (roleId: string) => admin.deleteRole(roleId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "rbac"] })
      showToast(t("toast.roleDeleted"), "success")
      setDeleting(null)
    },
    onError: (e: Error) => {
      // The refusals say what is wrong and what to do instead — a
      // built-in bundle is cloned, an in-use role is revoked first.
      if (e instanceof ApiError && e.code === "ROLE_IN_USE") {
        toastError(e, t("rbac.errRoleInUse"))
      } else if (e instanceof ApiError && e.code === "SYSTEM_ROLE_PROTECTED") {
        toastError(e, t("rbac.errSystemRole"))
      } else {
        toastError(e)
      }
    },
  })

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-2xl font-bold tracking-tight sr-only md:not-sr-only">{t("rbac.title")}</h1>
          <p className="text-muted-foreground">{t("rbac.subtitle")}</p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus className="h-4 w-4 mr-2" /> {t("rbac.newRole")}
        </Button>
      </div>

      <div className="flex flex-wrap gap-3">
        <Input
          placeholder={t("common.search")}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="w-full sm:w-64"
        />
      </div>

      <Card>
        <CardContent className="pt-6">
          {isLoading ? (
            <div className="h-32 animate-pulse rounded-lg bg-muted" />
          ) : roles.length === 0 ? (
            <ListEmptyState
              icon={ShieldCheck}
              title={t("rbac.empty")}
              description={t("rbac.emptyDesc")}
              action={{ label: t("rbac.newRole"), onClick: () => setCreating(true) }}
              filtered={search !== ""}
              filteredTitle={t("filter.noMatches")}
              filteredDescription={t("empty.filteredDesc")}
              clearLabel={t("common.clearFilters")}
              onClearFilters={() => setSearch("")}
            />
          ) : (
            <>
              <DataTable>
                <DataTableHeader>
                  <DataTableRow>
                    <DataTableHead>{t("common.name")}</DataTableHead>
                    <DataTableHead>{t("rbac.descriptionLabel")}</DataTableHead>
                    <DataTableHead>{t("common.type")}</DataTableHead>
                    <DataTableHead>{t("common.created")}</DataTableHead>
                    <DataTableHead className="w-24 text-right">{t("common.actions")}</DataTableHead>
                  </DataTableRow>
                </DataTableHeader>
                <DataTableBody>
                  {roles.map((role: Role) => (
                    <DataTableRow key={role.id}>
                      <DataTableCell>
                        <Link to={`/admin/rbac/roles/${role.id}`} className="font-medium hover:underline">
                          {role.name}
                        </Link>
                      </DataTableCell>
                      <DataTableCell className="text-muted-foreground">{role.description || "-"}</DataTableCell>
                      <DataTableCell>
                        {role.is_system ? (
                          <Badge className="bg-blue-100 text-blue-800">{t("rbac.systemBadge")}</Badge>
                        ) : (
                          <Badge className="bg-gray-100 text-gray-800">{t("rbac.customBadge")}</Badge>
                        )}
                      </DataTableCell>
                      <DataTableCell className="text-muted-foreground">{formatDate(role.created_at)}</DataTableCell>
                      <DataTableCell>
                        <div className="flex justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            disabled={role.is_system}
                            onClick={() => setDeleting(role)}
                            aria-label={t("common.delete")}
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

      {creating && <RoleCreateDialog open onClose={() => setCreating(false)} />}

      <AlertDialog open={!!deleting} onOpenChange={() => setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("common.delete")} "{deleting?.name}"?
            </AlertDialogTitle>
            <AlertDialogDescription>{t("rbac.deleteConfirm")}</AlertDialogDescription>
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

// RoleCreateDialog — create a role (and, prefilled, CLONE one: the
// built-in bundles refuse editing, so "clone to customize" starts
// here with the source's permissions copied in).
export function RoleCreateDialog({
  open,
  onClose,
  initial,
  onCreated,
}: {
  open: boolean
  onClose: () => void
  initial?: { name?: string; description?: string; permissions?: string[] }
  onCreated?: (role: Role) => void
}) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [name, setName] = useState(initial?.name ?? "")
  const [description, setDescription] = useState(initial?.description ?? "")
  const [permissions, setPermissions] = useState<string[]>(initial?.permissions ?? [])
  const [nameError, setNameError] = useState("")

  const { data: vocab } = useQuery({
    queryKey: ["admin", "rbac", "permissions"],
    queryFn: () => admin.listPermissions(),
    staleTime: 5 * 60_000,
  })

  const createMut = useMutation({
    mutationFn: () =>
      admin.createRole({ name: name.trim(), description: description.trim() || undefined, permissions }),
    onSuccess: (role) => {
      qc.invalidateQueries({ queryKey: ["admin", "rbac"] })
      showToast(t("toast.roleCreated"), "success")
      onCreated?.(role)
      onClose()
    },
    onError: (e: Error) => {
      // A name already taken — in any spelling — is 409 DUPLICATE;
      // the message goes next to the name field where it happened.
      if (e instanceof ApiError && e.code === "DUPLICATE") {
        setNameError(t("rbac.errNameTaken"))
        toastError(e, t("rbac.errNameTaken"))
      } else {
        toastError(e)
      }
    },
  })

  const submit = () => {
    if (!name.trim()) {
      setNameError(t("rbac.nameRequired"))
      return
    }
    setNameError("")
    createMut.mutate()
  }

  return (
    <Dialog open={open} onOpenChange={onClose}>
      <DialogContent className="max-w-2xl h-[min(720px,85vh)]">
        <DialogHeader>
          <DialogTitle>{initial?.name ? t("rbac.cloneRole") : t("rbac.createTitle")}</DialogTitle>
          <DialogDescription>{t("rbac.createDesc")}</DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="role-name">{t("rbac.nameLabel")}</Label>
            <Input
              id="role-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t("rbac.namePlaceholder")}
            />
            {nameError && <p className="text-xs text-destructive">{nameError}</p>}
          </div>
          <div className="space-y-2">
            <Label htmlFor="role-description">{t("rbac.descriptionLabel")}</Label>
            <Input
              id="role-description"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder={t("rbac.descriptionPlaceholder")}
            />
          </div>
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <Label>{t("rbac.permissionsTitle")}</Label>
              <span className="text-xs text-muted-foreground">
                {t("rbac.selectedCount", { count: permissions.length })}
              </span>
            </div>
            <PermissionPicker permissions={vocab?.permissions ?? []} value={permissions} onChange={setPermissions} />
          </div>
        </DialogBody>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button onClick={submit} disabled={createMut.isPending}>
            {t("common.create")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
