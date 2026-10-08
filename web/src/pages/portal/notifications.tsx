// Notifications — the full page (plan §88) behind the header bell:
// every row the account has earned, newest first, with paging and an
// unread-only filter. Text renders client-side from each row's
// title_key (see components/notification-bell); clicking a row marks
// it read and follows its deep link.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Bell, BellOff, CheckCheck } from "lucide-react"
import { useState } from "react"
import { useNavigate } from "react-router-dom"
import { ListEmptyState } from "@/components/empty-state"
import { NotificationRow } from "@/components/notification-bell"
import { SortBar } from "@/components/sort-bar"
import { toastError } from "@/components/toast"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { DataTablePagination, useServerPagination, useServerSort } from "@/components/ui/data-table"
import { Label } from "@/components/ui/label"
import { useI18n } from "@/i18n"
import { type NotificationItem, portal } from "@/lib/api"

export default function NotificationsPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [unreadOnly, setUnreadOnly] = useState(false)
  // Server-side sorting (plan §70): the feed reads newest first by
  // default; the SortBar re-asks the server for another order. A
  // re-sort joins the filters so the pager lands on the first page.
  const srt = useServerSort("created_at", "desc")
  const pg = useServerPagination(20, [unreadOnly, srt.sort, srt.order])

  const { data, isLoading } = useQuery({
    queryKey: ["portal", "notifications", "page", unreadOnly, srt.sort, srt.order, pg.page, pg.pageSize],
    queryFn: () => portal.listNotifications({ unread_only: unreadOnly || undefined, ...srt.params, ...pg.params }),
    refetchOnWindowFocus: true,
  })
  const { items: notifications, total, totalPages } = pg.from(data, data?.notifications)
  const unread = data?.unread_count ?? 0

  const invalidate = () => qc.invalidateQueries({ queryKey: ["portal", "notifications"] })
  const readMut = useMutation({
    mutationFn: (id: string) => portal.markNotificationRead(id),
    onSuccess: invalidate,
    onError: (e: Error) => toastError(e),
  })
  const readAllMut = useMutation({
    mutationFn: () => portal.markAllNotificationsRead(),
    onSuccess: invalidate,
    onError: (e: Error) => toastError(e),
  })

  // Click-through marks the row read and follows its deep link.
  const openRow = (n: NotificationItem) => {
    if (!n.read_at) readMut.mutate(n.id)
    if (n.link) navigate(n.link)
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-2xl font-bold tracking-tight sr-only md:not-sr-only">{t("notifications.title")}</h1>
          <p className="text-muted-foreground">{t("notifications.subtitle")}</p>
        </div>
        <Button variant="outline" disabled={unread === 0 || readAllMut.isPending} onClick={() => readAllMut.mutate()}>
          <CheckCheck className="h-4 w-4 mr-2" /> {t("notifications.markAllRead")}
        </Button>
      </div>

      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <input
            type="checkbox"
            id="notifications-unread-only"
            checked={unreadOnly}
            onChange={(e) => setUnreadOnly(e.target.checked)}
            className="h-4 w-4 rounded border-input accent-primary"
          />
          <Label htmlFor="notifications-unread-only" className="font-normal">
            {t("notifications.unreadOnly")}
          </Label>
        </div>
        {/* The feed is a card list, not a table, so its column
            headers live in this toolbar above the rows. */}
        <SortBar
          sort={srt}
          columns={[
            { column: "created_at", label: t("common.created"), firstOrder: "desc" },
            { column: "event", label: t("notifications.colEvent"), firstOrder: "asc" },
            { column: "priority", label: t("notifications.colPriority"), firstOrder: "desc" },
            { column: "read_at", label: t("notifications.colRead"), firstOrder: "desc" },
          ]}
        />
      </div>

      <Card>
        <CardContent className="pt-6">
          {isLoading ? (
            <div className="h-40 animate-pulse rounded-lg bg-muted" />
          ) : notifications.length === 0 ? (
            <ListEmptyState
              icon={unreadOnly ? BellOff : Bell}
              title={unreadOnly ? t("notifications.emptyUnread") : t("notifications.empty")}
              description={unreadOnly ? t("notifications.emptyUnreadDesc") : t("notifications.emptyDesc")}
              filtered={unreadOnly}
              filteredTitle={t("notifications.emptyUnread")}
              filteredDescription={t("notifications.emptyUnreadDesc")}
              clearLabel={t("notifications.showAll")}
              onClearFilters={() => setUnreadOnly(false)}
            />
          ) : (
            <div className="space-y-2">
              {notifications.map((n) => (
                <NotificationRow
                  key={n.id}
                  notification={n}
                  onOpen={openRow}
                  onMarkRead={(row) => readMut.mutate(row.id)}
                />
              ))}
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
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
