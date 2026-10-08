// RBAC — role detail (plan §8). One custom role and its three edit
// surfaces: the name/description, the permission set (family fieldsets
// with per-family toggles), and the users holding it. Built-in
// (system) bundles are read-only for name and permissions — the API
// refuses those edits with 409 SYSTEM_ROLE_PROTECTED and the page says
// so up front ("built-in — clone to customize").
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowLeft, Copy, Lock, UserPlus, Users } from "lucide-react"
import { useEffect, useState } from "react"
import { Link, useNavigate, useParams } from "react-router-dom"
import { EmptyState } from "@/components/empty-state"
import { PermissionPicker } from "@/components/permission-picker"
import { showToast, toastError } from "@/components/toast"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
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
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useI18n } from "@/i18n"
import { ApiError, admin } from "@/lib/api"
import { useDebounced } from "@/lib/use-debounced"
import { RoleCreateDialog } from "./rbac"

export default function RBACRolePage() {
  const { id = "" } = useParams()
  const { t } = useI18n()
  const navigate = useNavigate()
  const qc = useQueryClient()

  const {
    data: role,
    isLoading,
    isError,
  } = useQuery({
    queryKey: ["admin", "rbac", "role", id],
    queryFn: () => admin.getRole(id),
    enabled: !!id,
  })
  const isSystem = role?.is_system ?? false

  // The full §8 vocabulary — the checkboxes must show grants the role
  // does NOT have, or there is no way to add one. The role payload
  // only carries its own set.
  const { data: vocab } = useQuery({
    queryKey: ["admin", "rbac", "permissions"],
    queryFn: () => admin.listPermissions(),
    staleTime: 5 * 60_000,
  })

  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [permissions, setPermissions] = useState<string[]>([])
  const [nameError, setNameError] = useState("")
  const [cloning, setCloning] = useState(false)

  // Sync the form to the loaded role once per server payload — never
  // per render, or a keystroke would be overwritten by a refetch.
  useEffect(() => {
    if (!role) return
    setName(role.name)
    setDescription(role.description ?? "")
    setPermissions(role.permissions ?? [])
  }, [role])

  // A system bundle keeps its name: sending the name again could read
  // as a rename, so only the description travels for built-ins.
  const saveMetaMut = useMutation({
    mutationFn: () =>
      admin.updateRole(
        id,
        isSystem ? { description: description.trim() } : { name: name.trim(), description: description.trim() },
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "rbac"] })
      showToast(t("toast.roleUpdated"), "success")
    },
    onError: (e: Error) => {
      if (e instanceof ApiError && e.code === "DUPLICATE") {
        setNameError(t("rbac.errNameTaken"))
        toastError(e, t("rbac.errNameTaken"))
      } else if (e instanceof ApiError && e.code === "SYSTEM_ROLE_PROTECTED") {
        toastError(e, t("rbac.errSystemRole"))
      } else {
        toastError(e)
      }
    },
  })

  const savePermsMut = useMutation({
    mutationFn: (perms: string[]) => admin.setRolePermissions(id, perms),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "rbac"] })
      showToast(t("toast.rolePermissionsSaved"), "success")
    },
    onError: (e: Error) => {
      // The API refuses to re-permission a built-in bundle — the
      // banner already says to clone; repeat it with the request id.
      if (e instanceof ApiError && e.code === "SYSTEM_ROLE_PROTECTED") {
        toastError(e, t("rbac.errSystemRole"))
      } else {
        toastError(e)
      }
    },
  })

  // ─── User assignments ───
  // The assignment API is user-scoped (GET/POST/DELETE
  // /admin/rbac/users/:user_id/roles) with no role→users read, so
  // membership is resolved one cached call per listed user — bounded
  // by the page size, and shared with any other role page open.
  const [userSearchRaw, setUserSearchRaw] = useState("")
  const userSearch = useDebounced(userSearchRaw, 300)
  const upg = useServerPagination(10, [userSearch])
  const usersQuery = useQuery({
    queryKey: ["admin", "rbac", "role-users", userSearch, upg.page, upg.pageSize],
    queryFn: () => admin.listUsers({ search: userSearch || undefined, ...upg.params }),
  })
  const {
    items: users,
    total: userTotal,
    totalPages: userTotalPages,
  } = upg.from(usersQuery.data, usersQuery.data?.users)

  const membership = useQueries({
    queries: users.map((u) => ({
      queryKey: ["admin", "rbac", "user-roles", u.id],
      queryFn: () => admin.listUserRoles(u.id),
      staleTime: 30_000,
    })),
  })
  const holdsRole = (index: number): boolean => (membership[index]?.data?.roles ?? []).some((r) => r.id === id)

  const assignMut = useMutation({
    mutationFn: (userId: string) => admin.assignUserRole(userId, id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "rbac"] })
      showToast(t("toast.roleAssigned"), "success")
    },
    onError: (e: Error) => toastError(e),
  })
  const revokeMut = useMutation({
    mutationFn: (userId: string) => admin.revokeUserRole(userId, id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "rbac"] })
      showToast(t("toast.roleRevoked"), "success")
    },
    onError: (e: Error) => toastError(e),
  })

  if (isError) {
    return (
      <EmptyState
        icon={Users}
        title={t("rbac.notFound")}
        description={t("rbac.notFoundDesc")}
        action={{ label: t("rbac.backToRoles"), to: "/admin/rbac" }}
      />
    )
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <Link to="/admin/rbac" className="mb-1 flex items-center gap-1 text-sm text-muted-foreground hover:underline">
            <ArrowLeft className="h-3.5 w-3.5" /> {t("rbac.backToRoles")}
          </Link>
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="text-2xl font-bold tracking-tight">{role?.name ?? t("common.loading")}</h1>
            {isSystem && <Badge className="bg-blue-100 text-blue-800">{t("rbac.systemBadge")}</Badge>}
          </div>
          <p className="text-muted-foreground">{t("rbac.detailSubtitle")}</p>
        </div>
        <Button variant="outline" onClick={() => setCloning(true)} disabled={!role}>
          <Copy className="h-4 w-4 mr-2" /> {t("rbac.cloneRole")}
        </Button>
      </div>

      {isSystem && (
        <div className="flex items-start gap-3 rounded-lg border border-blue-200 bg-blue-50 px-4 py-3 text-sm text-blue-900">
          <Lock className="mt-0.5 h-4 w-4 shrink-0" />
          <p>{t("rbac.systemBanner")}</p>
        </div>
      )}

      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("rbac.detailsTitle")}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="role-name">{t("rbac.nameLabel")}</Label>
            <Input id="role-name" value={name} disabled={isSystem} onChange={(e) => setName(e.target.value)} />
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
          <div className="flex justify-end">
            <Button
              onClick={() => {
                if (!isSystem && !name.trim()) {
                  setNameError(t("rbac.nameRequired"))
                  return
                }
                setNameError("")
                saveMetaMut.mutate()
              }}
              disabled={saveMetaMut.isPending || isLoading}
            >
              {t("common.save")}
            </Button>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <CardTitle className="text-base">{t("rbac.permissionsTitle")}</CardTitle>
            <div className="flex items-center gap-3">
              <span className="text-xs text-muted-foreground">
                {t("rbac.selectedCount", { count: permissions.length })}
              </span>
              <Button
                size="sm"
                disabled={isSystem || savePermsMut.isPending || isLoading}
                onClick={() => savePermsMut.mutate(permissions)}
              >
                {t("rbac.savePermissions")}
              </Button>
            </div>
          </div>
        </CardHeader>
        <CardContent>
          <PermissionPicker
            permissions={vocab?.permissions ?? []}
            value={permissions}
            onChange={setPermissions}
            disabled={isSystem}
          />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <CardTitle className="text-base">{t("rbac.usersTitle")}</CardTitle>
            <p className="text-xs text-muted-foreground">{t("rbac.usersSubtitle")}</p>
          </div>
        </CardHeader>
        <CardContent className="space-y-4">
          <Input
            placeholder={t("rbac.searchUsers")}
            value={userSearchRaw}
            onChange={(e) => setUserSearchRaw(e.target.value)}
            className="w-full sm:w-72"
          />
          {usersQuery.isLoading ? (
            <div className="h-24 animate-pulse rounded-lg bg-muted" />
          ) : users.length === 0 ? (
            <EmptyState icon={Users} title={t("rbac.noUsers")} description={t("empty.filteredDesc")} />
          ) : (
            <>
              <DataTable>
                <DataTableHeader>
                  <DataTableRow>
                    <DataTableHead>{t("common.name")}</DataTableHead>
                    <DataTableHead>{t("common.email")}</DataTableHead>
                    <DataTableHead>{t("rbac.thisRole")}</DataTableHead>
                    <DataTableHead className="w-28 text-right">{t("common.actions")}</DataTableHead>
                  </DataTableRow>
                </DataTableHeader>
                <DataTableBody>
                  {users.map((u, i) => {
                    const assigned = holdsRole(i)
                    return (
                      <DataTableRow key={u.id}>
                        <DataTableCell className="font-medium">{u.name || "-"}</DataTableCell>
                        <DataTableCell className="text-muted-foreground">{u.email}</DataTableCell>
                        <DataTableCell>
                          {assigned ? (
                            <Badge className="bg-emerald-100 text-emerald-800">{t("rbac.assigned")}</Badge>
                          ) : (
                            <span className="text-muted-foreground">-</span>
                          )}
                        </DataTableCell>
                        <DataTableCell>
                          <div className="flex justify-end">
                            {assigned ? (
                              <Button
                                variant="outline"
                                size="sm"
                                disabled={revokeMut.isPending}
                                onClick={() => revokeMut.mutate(u.id)}
                              >
                                {t("rbac.revoke")}
                              </Button>
                            ) : (
                              <Button size="sm" disabled={assignMut.isPending} onClick={() => assignMut.mutate(u.id)}>
                                <UserPlus className="h-3.5 w-3.5 mr-1" /> {t("rbac.assign")}
                              </Button>
                            )}
                          </div>
                        </DataTableCell>
                      </DataTableRow>
                    )
                  })}
                </DataTableBody>
              </DataTable>
              {userTotal > 0 && (
                <DataTablePagination
                  page={upg.page}
                  totalPages={userTotalPages}
                  total={userTotal}
                  pageSize={upg.pageSize}
                  onPageChange={upg.setPage}
                  onPageSizeChange={upg.setPageSize}
                />
              )}
            </>
          )}
        </CardContent>
      </Card>

      {cloning && role && (
        <RoleCreateDialog
          open
          onClose={() => setCloning(false)}
          initial={{
            name: t("rbac.cloneName", { name: role.name }),
            description: role.description,
            permissions: role.permissions ?? [],
          }}
          onCreated={(created) => navigate(`/admin/rbac/roles/${created.id}`)}
        />
      )}
    </div>
  )
}
