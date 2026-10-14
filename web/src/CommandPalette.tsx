import { useEffect, useMemo, useRef, useState } from 'react'

/**
 * The command palette.
 *
 * Cmd-K, type, enter: a reviewer working a queue against regulatory deadlines should be
 * one gesture from any case, policy or report rather than three clicks away.
 *
 * Two deliberate restrictions:
 *
 *   - Nothing here mutates. It navigates and it searches. A decision about somebody's
 *     care is never one fuzzy match away from another case's.
 *   - Nothing here is hidden behind it. Every action is reachable another way, so it
 *     is an accelerator rather than a place where features live.
 */
export interface Command {
  id: string
  /** label is what is shown and what is matched against. */
  label: string
  /** group orders and heads the sections. */
  group: string
  /** hint appears on the right, usually a shortcut or a value. */
  hint?: string
  /** keywords are matched as well as the label, for things people call by another name. */
  keywords?: string[]
  run: () => void
}

export function CommandPalette({
  commands,
  open,
  onClose,
}: {
  commands: Command[]
  open: boolean
  onClose: () => void
}) {
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLDivElement>(null)

  // Reset on open rather than on close, so the closing animation does not show the
  // list flickering back to its unfiltered state.
  useEffect(() => {
    if (open) {
      setQuery('')
      setActive(0)
      // Focused after paint, or the browser puts the caret nowhere.
      requestAnimationFrame(() => inputRef.current?.focus())
    }
  }, [open])

  const matches = useMemo(() => rank(commands, query), [commands, query])

  useEffect(() => {
    if (active >= matches.length) setActive(0)
  }, [matches.length, active])

  // Keeps the highlighted row on screen when moving with the keyboard, which is the
  // only way it will be moved by anybody using this.
  useEffect(() => {
    const el = listRef.current?.querySelector<HTMLElement>(`[data-index="${active}"]`)
    el?.scrollIntoView({ block: 'nearest' })
  }, [active])

  if (!open) return null

  const onKeyDown = (e: React.KeyboardEvent) => {
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        setActive((i) => (matches.length === 0 ? 0 : (i + 1) % matches.length))
        break
      case 'ArrowUp':
        e.preventDefault()
        setActive((i) => (matches.length === 0 ? 0 : (i - 1 + matches.length) % matches.length))
        break
      case 'Enter': {
        e.preventDefault()
        const chosen = matches[active]
        if (chosen) {
          onClose()
          chosen.run()
        }
        break
      }
      case 'Escape':
        e.preventDefault()
        onClose()
        break
    }
  }

  let lastGroup = ''

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-slate-950/70 pt-[12vh] backdrop-blur-sm"
      onClick={onClose}
      role="presentation"
    >
      <div
        className="w-full max-w-xl overflow-hidden rounded-xl border border-slate-700 bg-slate-900 shadow-2xl"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label="Command palette"
      >
        <div className="flex items-center gap-2 border-b border-slate-800 px-4">
          <span aria-hidden className="text-slate-400">
            ⌘
          </span>
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => {
              setQuery(e.target.value)
              setActive(0)
            }}
            onKeyDown={onKeyDown}
            placeholder="Go to a case, a policy, a screen…"
            className="w-full bg-transparent py-3.5 text-sm text-slate-100 placeholder:text-slate-400 focus:outline-none"
            aria-autocomplete="list"
            aria-controls="palette-results"
          />
          <kbd className="rounded border border-slate-700 px-1.5 py-0.5 text-[10px] text-slate-500">
            esc
          </kbd>
        </div>

        <div
          ref={listRef}
          id="palette-results"
          role="listbox"
          className="max-h-80 overflow-y-auto py-1.5"
        >
          {matches.length === 0 ? (
            <p className="px-4 py-6 text-center text-sm text-slate-400">
              Nothing matches “{query}”.
            </p>
          ) : (
            matches.map((c, i) => {
              const heading = c.group !== lastGroup
              lastGroup = c.group
              return (
                <div key={c.id}>
                  {heading && (
                    <div className="px-4 pb-1 pt-2 text-[10px] font-medium uppercase tracking-wide text-slate-400">
                      {c.group}
                    </div>
                  )}
                  <button
                    data-index={i}
                    role="option"
                    aria-selected={i === active}
                    onMouseEnter={() => setActive(i)}
                    onClick={() => {
                      onClose()
                      c.run()
                    }}
                    className={`flex w-full items-center gap-3 px-4 py-2 text-left text-sm transition ${
                      i === active
                        ? 'bg-slate-800 text-slate-100'
                        : 'text-slate-400 hover:text-slate-200'
                    }`}
                  >
                    <span className="truncate">{c.label}</span>
                    {c.hint && (
                      <span className="ml-auto shrink-0 font-mono text-xs text-slate-400">
                        {c.hint}
                      </span>
                    )}
                  </button>
                </div>
              )
            })
          )}
        </div>

        <div className="flex items-center gap-3 border-t border-slate-800 px-4 py-2 text-[10px] text-slate-400">
          <span>↑↓ move</span>
          <span>↵ open</span>
          <span className="ml-auto">navigation only — nothing here decides anything</span>
        </div>
      </div>
    </div>
  )
}

/**
 * rank orders commands against a query.
 *
 * Subsequence matching rather than substring, so "mrl" finds "MRI lumbar" — that is
 * the whole reason to type instead of clicking. Scored so that a match at the start
 * of a word beats one in the middle, because "mri" should find the MRI policy before
 * it finds anything with "mri" buried inside it.
 */
export function rank(commands: Command[], query: string): Command[] {
  const q = query.trim().toLowerCase()
  if (q === '') {
    return commands
  }

  const scored: { c: Command; score: number }[] = []

  for (const c of commands) {
    const best = Math.max(
      score(c.label.toLowerCase(), q),
      ...(c.keywords ?? []).map((k) => score(k.toLowerCase(), q) - 1),
    )
    if (best > 0) {
      scored.push({ c, score: best })
    }
  }

  // Stable within a score, so the list does not reshuffle as somebody types a
  // character that changes nothing.
  scored.sort((a, b) => b.score - a.score)
  return scored.map((s) => s.c)
}

function score(text: string, q: string): number {
  if (text === q) return 1000
  if (text.startsWith(q)) return 500

  const at = text.indexOf(q)
  if (at === 0) return 400
  if (at > 0) {
    // A whole-word match is worth more than one inside a word.
    return text[at - 1] === ' ' || text[at - 1] === '·' ? 300 : 200
  }

  // Subsequence: every character of the query in order, not necessarily adjacent.
  let ti = 0
  let hits = 0
  let wordStarts = 0
  for (const ch of q) {
    const found = text.indexOf(ch, ti)
    if (found === -1) return 0
    if (found === 0 || text[found - 1] === ' ' || text[found - 1] === '·') {
      wordStarts++
    }
    hits++
    ti = found + 1
  }
  // Shorter targets win on an equal match.
  return 50 + wordStarts * 10 + Math.max(0, 20 - text.length / 4) + hits
}
