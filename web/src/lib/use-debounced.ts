import { useEffect, useState } from "react"

// Holds a value back until it has stopped changing, so a picker asks
// the server once per pause in typing rather than once per keystroke.
export function useDebounced<T>(value: T, ms = 250): T {
  const [settled, setSettled] = useState(value)
  useEffect(() => {
    const timer = setTimeout(() => setSettled(value), ms)
    return () => clearTimeout(timer)
  }, [value, ms])
  return settled
}
