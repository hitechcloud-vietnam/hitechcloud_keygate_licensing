// Semantic version ordering, the same as the server's: numeric
// major.minor.patch, then a release ranks above its pre-releases, and
// pre-release identifiers compare numerically or lexically per SemVer 2.0.

export function parseSemver(v: string): [number, number, number, string] | null {
  const m = /^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/.exec(v)
  if (!m) return null
  for (const part of [m[1], m[2], m[3]]) {
    if (part.length > 1 && part[0] === "0") return null
  }
  const pre = m[4] ?? ""
  if (pre) {
    for (const id of pre.split(".")) {
      if (id === "") return null
      if (/^\d+$/.test(id) && id.length > 1 && id[0] === "0") return null
    }
  }
  return [Number(m[1]), Number(m[2]), Number(m[3]), pre]
}

export function compareSemver(a: string, b: string): number {
  const pa = parseSemver(a)
  const pb = parseSemver(b)
  if (!pa && !pb) return 0
  if (!pa) return -1
  if (!pb) return 1
  for (let i = 0; i < 3; i++) {
    if (pa[i] !== pb[i]) return (pa[i] as number) - (pb[i] as number)
  }
  const preA = pa[3] as string
  const preB = pb[3] as string
  if (preA === preB) return 0
  if (preA === "") return 1
  if (preB === "") return -1
  const partsA = preA.split(".")
  const partsB = preB.split(".")
  const len = Math.max(partsA.length, partsB.length)
  for (let i = 0; i < len; i++) {
    const ai = partsA[i]
    const bi = partsB[i]
    if (ai === undefined) return -1
    if (bi === undefined) return 1
    const aNum = /^\d+$/.test(ai)
    const bNum = /^\d+$/.test(bi)
    if (aNum && bNum) {
      const diff = Number(ai) - Number(bi)
      if (diff !== 0) return diff
    } else if (aNum) {
      return -1
    } else if (bNum) {
      return 1
    } else if (ai !== bi) {
      return ai < bi ? -1 : 1
    }
  }
  return 0
}
