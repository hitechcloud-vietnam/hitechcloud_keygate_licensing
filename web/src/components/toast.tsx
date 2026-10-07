import { createContext, type ReactNode, useCallback, useContext, useEffect, useRef, useState } from "react"
import { createPortal } from "react-dom"
import { useI18nOptional } from "@/i18n"

interface Toast {
  id: string
  message: string
  type: "error" | "success"
}

interface ToastContextType {
  addToast: (message: string, type?: "error" | "success") => void
}

// An error is worth reading — the message usually says what to change —
// so it stays three times as long as a "saved" confirmation, and either
// can be sent away early with the close button.
const ERROR_MS = 15_000
const SUCCESS_MS = 5_000

const ToastContext = createContext<ToastContextType>({ addToast: () => {} })

export function ToastProvider({ children }: { children: ReactNode }) {
  const { t } = useI18nOptional()
  const [toasts, setToasts] = useState<Toast[]>([])
  const timers = useRef(new Map<string, ReturnType<typeof setTimeout>>())

  const dismiss = useCallback((id: string) => {
    const timer = timers.current.get(id)
    if (timer) {
      clearTimeout(timer)
      timers.current.delete(id)
    }
    setToasts((prev) => prev.filter((toast) => toast.id !== id))
  }, [])

  const addToast = useCallback(
    (message: string, type: "error" | "success" = "error") => {
      // The id is the message itself: a failure that repeats — two saves
      // rejected by the same rule, a retried request — shows once and has
      // its timer restarted, instead of stacking copies that push the
      // first one out of sight.
      const id = `${type}:${message}`
      setToasts((prev) => (prev.some((toast) => toast.id === id) ? prev : [...prev, { id, message, type }]))
      const existing = timers.current.get(id)
      if (existing) clearTimeout(existing)
      timers.current.set(
        id,
        setTimeout(() => dismiss(id), type === "error" ? ERROR_MS : SUCCESS_MS),
      )
    },
    [dismiss],
  )

  useEffect(() => {
    const pending = timers.current
    return () => {
      for (const timer of pending.values()) clearTimeout(timer)
      pending.clear()
    }
  }, [])

  return (
    <ToastContext.Provider value={{ addToast }}>
      {children}
      {/* Rendered into <body> and above the z-50 the dialogs use: a toast
          raised by a dialog was landing under that dialog's backdrop,
          dimmed and half-covered. */}
      {createPortal(
        <div className="pointer-events-none fixed bottom-4 right-4 z-[100] flex max-w-[calc(100vw-2rem)] flex-col items-end gap-2 sm:max-w-sm">
          {toasts.map((toast) => (
            <div
              key={toast.id}
              role={toast.type === "error" ? "alert" : "status"}
              className={`pointer-events-auto flex w-full items-start gap-2 rounded-lg border px-4 py-3 text-sm shadow-lg animate-in slide-in-from-bottom-2 ${
                toast.type === "error"
                  ? "border-red-200 bg-red-50 text-red-800"
                  : "border-emerald-200 bg-emerald-50 text-emerald-800"
              }`}
            >
              <span className="min-w-0 flex-1 break-words">{toast.message}</span>
              <button
                type="button"
                onClick={() => dismiss(toast.id)}
                aria-label={t("toast.dismiss")}
                className="-mr-1 shrink-0 rounded px-1 leading-none opacity-60 transition-opacity hover:opacity-100"
              >
                ×
              </button>
            </div>
          ))}
        </div>,
        document.body,
      )}
    </ToastContext.Provider>
  )
}

export function useToast() {
  return useContext(ToastContext)
}

// Global reference for use outside React (in QueryClient config)
let globalAddToast: ((message: string, type?: "error" | "success") => void) | null = null

export function setGlobalToast(fn: typeof globalAddToast) {
  globalAddToast = fn
}

export function showToast(message: string, type: "error" | "success" = "error") {
  if (globalAddToast) globalAddToast(message, type)
}

/** Bridge component that wires up the global toast ref inside the React tree */
export function ToastBridge() {
  const { addToast } = useToast()
  useEffect(() => {
    setGlobalToast(addToast)
  }, [addToast])
  return null
}
