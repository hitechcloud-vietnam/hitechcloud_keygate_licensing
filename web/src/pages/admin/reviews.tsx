import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Check, Eye, Reply, Star, Trash2, X } from "lucide-react"
import { useState } from "react"
import { ListEmptyState } from "@/components/empty-state"
import { StarRating } from "@/components/star-rating"
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Sheet, SheetContent } from "@/components/ui/sheet"
import { type TranslationKeys, useI18n } from "@/i18n"
import { ApiError, admin, type Review } from "@/lib/api"
import { useDebounced } from "@/lib/use-debounced"
import { formatDate } from "@/lib/utils"

const REVIEW_STATUSES = ["pending", "approved", "rejected"] as const

// The three moderation statuses each read differently on a badge:
// waiting to be seen is not published, and withheld is not waiting.
function reviewStatusColor(status: string): string {
  switch (status) {
    case "approved":
      return "bg-emerald-100 text-emerald-800"
    case "pending":
      return "bg-amber-100 text-amber-800"
    case "rejected":
      return "bg-red-100 text-red-700"
    default:
      return "bg-gray-100 text-gray-800"
  }
}

// Review moderation queue (marketplace content): GET /admin/reviews
// with status and product filters, the state-machine actions
// (approve / reject), the vendor's public answer (reply — "" clears),
// and delete for spam and abuse. Illegal moves answer 409
// REVIEW_TRANSITION_INVALID and surface as a toast instead of a
// silent "success", so a double-click is visible rather than blurry.
export default function AdminReviewsPage() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [statusFilter, setStatusFilter] = useState("")
  const [productInput, setProductInput] = useState("")
  const productFilter = useDebounced(productInput, 300)
  const pg = useServerPagination(10, [statusFilter, productFilter])
  const { data, isLoading } = useQuery({
    queryKey: ["admin", "reviews", statusFilter, productFilter, pg.page, pg.pageSize],
    queryFn: () =>
      admin.listReviews({ status: statusFilter || undefined, product_id: productFilter || undefined, ...pg.params }),
  })

  // The review rows carry only product_id, so the product column's
  // name comes from one cached catalog lookup — never a per-row fetch.
  const productsQuery = useQuery({
    queryKey: ["admin", "products", "review-lookup"],
    queryFn: () => admin.listProducts({ limit: 200 }),
    staleTime: 60_000,
  })
  const productName = (id: string) => productsQuery.data?.products.find((p) => p.id === id)?.name

  const [detail, setDetail] = useState<Review | null>(null)
  const [replying, setReplying] = useState<Review | null>(null)
  const [deleting, setDeleting] = useState<Review | null>(null)

  const { items: reviews, total, totalPages } = pg.from(data, data?.reviews)

  const refresh = () => qc.invalidateQueries({ queryKey: ["admin", "reviews"] })

  // The state machine is model.CanTransitionReview and nothing else
  // moves: pending → approved | rejected, approved ↔ rejected.
  const moderateMut = useMutation({
    mutationFn: ({ id, action }: { id: string; action: "approve" | "reject" }) =>
      action === "approve" ? admin.approveReview(id) : admin.rejectReview(id),
    onSuccess: (res, vars) => {
      refresh()
      // Keep the open drawer on the row the server now holds.
      setDetail((d) => (d && res && d.id === res.id ? res : d))
      showToast(vars.action === "approve" ? t("toast.reviewApproved") : t("toast.reviewRejected"), "success")
    },
    onError: (e: Error) => {
      // A move the state machine refuses is a race (someone else
      // moderated first), not a mystery: say so, and keep the request
      // id on every refusal for the support reference.
      if (e instanceof ApiError && e.code === "REVIEW_TRANSITION_INVALID") {
        toastError(e, t("reviews.errTransition"))
      } else {
        toastError(e)
      }
    },
  })

  // An empty reply clears the answer — the PUT body always names the
  // field (admin_reply is required), "" is the documented clear.
  const replyMut = useMutation({
    mutationFn: ({ id, reply }: { id: string; reply: string }) => admin.replyReview(id, reply),
    onSuccess: (res) => {
      refresh()
      setDetail((d) => (d && res && d.id === res.id ? res : d))
      showToast(t("toast.reviewReplied"), "success")
      setReplying(null)
    },
    onError: (e: Error) => toastError(e),
  })

  const deleteMut = useMutation({
    mutationFn: (id: string) => admin.deleteReview(id),
    onSuccess: () => {
      refresh()
      showToast(t("toast.reviewDeleted"), "success")
      setDeleting(null)
      setDetail(null)
    },
    onError: (e: Error) => toastError(e),
  })

  return (
    <div className="space-y-6">
      <div className="min-w-0">
        <h1 className="text-2xl font-bold tracking-tight sr-only md:not-sr-only">{t("reviews.title")}</h1>
        <p className="text-muted-foreground">{t("reviews.subtitle")}</p>
      </div>

      <div className="flex flex-wrap gap-3">
        <Input
          placeholder={t("reviews.productFilterPlaceholder")}
          value={productInput}
          onChange={(e) => setProductInput(e.target.value)}
          className="w-full sm:w-64"
        />
        <Select value={statusFilter} onValueChange={(v) => setStatusFilter(v === "all" ? "" : v)}>
          <SelectTrigger className="w-full sm:w-48">
            <SelectValue placeholder={t("filter.allStatuses")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("filter.allStatuses")}</SelectItem>
            {REVIEW_STATUSES.map((s) => (
              <SelectItem key={s} value={s}>
                {t(`status.${s}` as TranslationKeys)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      <Card>
        <CardContent className="pt-6">
          {isLoading ? (
            <div className="h-32 animate-pulse bg-muted rounded-lg" />
          ) : reviews.length === 0 ? (
            // The queue fills from customer reviews, so an empty view
            // is either "none written yet" or "filtered to nothing".
            <ListEmptyState
              icon={Star}
              title={t("empty.reviews.title")}
              description={t("empty.reviews.desc")}
              filtered={statusFilter !== "" || productFilter !== ""}
              filteredTitle={t("filter.noMatches")}
              filteredDescription={t("empty.filteredDesc")}
              clearLabel={t("common.clearFilters")}
              onClearFilters={() => {
                setStatusFilter("")
                setProductInput("")
              }}
            />
          ) : (
            <>
              <DataTable>
                <DataTableHeader>
                  <DataTableRow>
                    <DataTableHead>{t("common.product")}</DataTableHead>
                    <DataTableHead>{t("reviews.colAuthor")}</DataTableHead>
                    <DataTableHead>{t("reviews.colRating")}</DataTableHead>
                    <DataTableHead>{t("reviews.colReview")}</DataTableHead>
                    <DataTableHead>{t("common.status")}</DataTableHead>
                    <DataTableHead>{t("common.created")}</DataTableHead>
                    <DataTableHead className="w-40 text-right">{t("common.actions")}</DataTableHead>
                  </DataTableRow>
                </DataTableHeader>
                <DataTableBody>
                  {reviews.map((r: Review) => (
                    <DataTableRow key={r.id}>
                      <DataTableCell>
                        <div className="font-medium">{productName(r.product_id) || r.product_id}</div>
                        {productName(r.product_id) && (
                          <code className="text-xs text-muted-foreground">{r.product_id}</code>
                        )}
                      </DataTableCell>
                      <DataTableCell>
                        <div>{r.customer_name || t("reviews.anonymous")}</div>
                        <div className="text-xs text-muted-foreground">{r.customer_email}</div>
                      </DataTableCell>
                      <DataTableCell>
                        <StarRating valueBps={r.rating * 10000} size="sm" />
                      </DataTableCell>
                      <DataTableCell className="max-w-56">
                        <button
                          type="button"
                          className="block w-full truncate text-left hover:underline"
                          onClick={() => setDetail(r)}
                        >
                          {r.title || r.body}
                        </button>
                      </DataTableCell>
                      <DataTableCell>
                        <Badge className={reviewStatusColor(r.status)}>
                          {t(`status.${r.status}` as TranslationKeys)}
                        </Badge>
                      </DataTableCell>
                      <DataTableCell className="text-muted-foreground">{formatDate(r.created_at)}</DataTableCell>
                      <DataTableCell>
                        <div className="flex justify-end gap-1">
                          {r.status !== "approved" && (
                            <Button
                              variant="ghost"
                              size="icon"
                              title={t("reviews.approve")}
                              disabled={moderateMut.isPending}
                              onClick={() => moderateMut.mutate({ id: r.id, action: "approve" })}
                            >
                              <Check className="h-4 w-4 text-emerald-600" />
                            </Button>
                          )}
                          {r.status !== "rejected" && (
                            <Button
                              variant="ghost"
                              size="icon"
                              title={t("reviews.reject")}
                              disabled={moderateMut.isPending}
                              onClick={() => moderateMut.mutate({ id: r.id, action: "reject" })}
                            >
                              <X className="h-4 w-4 text-destructive" />
                            </Button>
                          )}
                          <Button variant="ghost" size="icon" title={t("reviews.reply")} onClick={() => setReplying(r)}>
                            <Reply className="h-4 w-4" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            title={t("reviews.detailTitle")}
                            onClick={() => setDetail(r)}
                          >
                            <Eye className="h-4 w-4" />
                          </Button>
                          <Button variant="ghost" size="icon" title={t("common.delete")} onClick={() => setDeleting(r)}>
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

      {/* Detail drawer: the whole review, unclipped */}
      <Sheet
        open={!!detail}
        onOpenChange={(open) => {
          if (!open) setDetail(null)
        }}
      >
        <SheetContent side="right" label={t("reviews.detailTitle")} className="w-[min(480px,90vw)] overflow-y-auto">
          {detail && (
            <div className="space-y-4 p-6 pt-10">
              <div className="flex flex-wrap items-center gap-2">
                <StarRating valueBps={detail.rating * 10000} />
                <Badge className={reviewStatusColor(detail.status)}>
                  {t(`status.${detail.status}` as TranslationKeys)}
                </Badge>
              </div>
              <div>
                <h2 className="text-lg font-semibold">{detail.title || t("reviews.colReview")}</h2>
                <p className="text-sm text-muted-foreground">
                  {detail.customer_name || t("reviews.anonymous")} · {detail.customer_email}
                </p>
                <p className="text-xs text-muted-foreground">
                  {t("common.product")}: {productName(detail.product_id) || detail.product_id} · {t("common.created")}:{" "}
                  {formatDate(detail.created_at)}
                </p>
              </div>
              <p className="whitespace-pre-wrap text-sm leading-relaxed">{detail.body}</p>
              {detail.admin_reply && (
                <div className="rounded-md border bg-muted/50 p-3">
                  <p className="text-xs font-medium">{t("reviews.adminReplyLabel")}</p>
                  <p className="mt-1 whitespace-pre-wrap text-sm text-muted-foreground">{detail.admin_reply}</p>
                </div>
              )}
              <div className="flex flex-wrap gap-2">
                <Button variant="outline" size="sm" onClick={() => setReplying(detail)}>
                  <Reply className="h-4 w-4 mr-1" /> {t("reviews.reply")}
                </Button>
                {detail.status !== "approved" && (
                  <Button
                    size="sm"
                    disabled={moderateMut.isPending}
                    onClick={() => moderateMut.mutate({ id: detail.id, action: "approve" })}
                  >
                    {t("reviews.approve")}
                  </Button>
                )}
                {detail.status !== "rejected" && (
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={moderateMut.isPending}
                    onClick={() => moderateMut.mutate({ id: detail.id, action: "reject" })}
                  >
                    {t("reviews.reject")}
                  </Button>
                )}
              </div>
            </div>
          )}
        </SheetContent>
      </Sheet>

      {replying && (
        <ReplyDialog
          review={replying}
          onClose={() => setReplying(null)}
          pending={replyMut.isPending}
          onSubmit={(reply) => replyMut.mutate({ id: replying.id, reply })}
        />
      )}

      <AlertDialog open={!!deleting} onOpenChange={() => setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("common.delete")}?</AlertDialogTitle>
            <AlertDialogDescription>{t("reviews.deleteConfirm")}</AlertDialogDescription>
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

// ReplyDialog — the vendor's public answer under one review. Empty
// input clears the stored reply; the reply can be written at any
// moderation status (answering a pending review before publication is
// legitimate).
function ReplyDialog({
  review,
  onClose,
  onSubmit,
  pending,
}: {
  review: Review
  onClose: () => void
  onSubmit: (reply: string) => void
  pending: boolean
}) {
  const { t } = useI18n()
  const [reply, setReply] = useState(review.admin_reply || "")

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("reviews.replyTitle")}</DialogTitle>
          <DialogDescription>{t("reviews.replyDesc")}</DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            onSubmit(reply.trim())
          }}
          className="flex min-h-0 flex-1 flex-col gap-4"
        >
          <DialogBody className="space-y-4">
            <div className="rounded-md border bg-muted/50 p-3">
              <div className="flex items-center gap-2">
                <StarRating valueBps={review.rating * 10000} size="sm" />
                <span className="text-xs text-muted-foreground">{review.customer_name || t("reviews.anonymous")}</span>
              </div>
              {review.title && <p className="mt-1 text-sm font-medium">{review.title}</p>}
              <p className="mt-1 whitespace-pre-wrap text-sm text-muted-foreground">{review.body}</p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="admin-reply">{t("reviews.replyLabel")}</Label>
              <textarea
                id="admin-reply"
                value={reply}
                maxLength={4000}
                rows={4}
                onChange={(e) => setReply(e.target.value)}
                className="flex w-full rounded-md border border-input bg-background px-3 py-2 text-sm shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
              />
            </div>
          </DialogBody>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={pending}>
              {pending ? t("common.loading") : t("common.save")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
