import {
  Background,
  Controls,
  MarkerType,
  ReactFlow,
  type Edge,
  type Node,
  type NodeMouseHandler,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { usePolicyDetailQuery, usePolicyMapQuery } from './store'
import type { Argument, DecisionMap, PolicyDetail } from './types'

const outcomeName: Record<string, string> = { a: 'Approved', d: 'Denied', p: 'Pended', x: 'Policy defect' }
const outcomeVar: Record<string, string> = { a: '--ok', d: '--bad', p: '--due', x: '--info' }

/** The column each argument sits in: evidence first, decisions last, every criterion right of what defeats it. */
export function layers(p: Pick<PolicyDetail, 'arguments' | 'attacks'>): Map<string, number> {
  const depth = new Map<string, number>()
  const attackers = new Map<string, string[]>()
  for (const a of p.attacks) attackers.set(a.to, [...(attackers.get(a.to) ?? []), a.from])
  const kind = new Map(p.arguments.map((a) => [a.name, a.kind]))
  const visit = (n: string, seen: Set<string>): number => {
    if (depth.has(n)) return depth.get(n)!
    if (kind.get(n) === 'evidence' || seen.has(n)) return 0
    seen.add(n)
    let d = 0
    for (const f of attackers.get(n) ?? []) d = Math.max(d, visit(f, seen) + 1)
    seen.delete(n)
    depth.set(n, d)
    return d
  }
  for (const a of p.arguments) if (a.kind !== 'decision') visit(a.name, new Set())
  for (const a of p.arguments) if (a.kind === 'evidence') depth.set(a.name, 0)
  const last = Math.max(1, ...[...depth.values()]) + 1
  for (const a of p.arguments) if (a.kind === 'decision') depth.set(a.name, last)
  return depth
}

/** The policy as a diagram: who defeats whom, left to right, from the evidence to the decisions. */
function Diagram({ p }: { p: PolicyDetail }) {
  const [picked, setPicked] = useState<Argument | null>(null)
  const { nodes, edges } = useMemo(() => {
    const col = layers(p)
    const rows = new Map<number, number>()
    const nodes: Node[] = p.arguments.map((a) => {
      const c = col.get(a.name) ?? 0
      const r = rows.get(c) ?? 0
      rows.set(c, r + 1)
      return {
        id: a.name,
        position: { x: c * 300, y: r * 92 },
        data: { label: a.text },
        className: `pm-node pm-${a.kind}${a.verdict ? ' pm-' + a.verdict : ''}`,
        sourcePosition: 'right',
        targetPosition: 'left',
        ariaLabel: `${a.kind}: ${a.text}`,
      } as Node
    })
    // Centre each column on the tallest.
    const tallest = Math.max(...rows.values())
    for (const n of nodes) {
      const c = col.get(n.id) ?? 0
      n.position.y += ((tallest - (rows.get(c) ?? 0)) * 92) / 2
    }
    const edges: Edge[] = p.attacks.map((a) => ({
      id: `${a.from}->${a.to}`,
      source: a.from,
      target: a.to,
      className: 'pm-edge',
      markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16 },
    }))
    return { nodes, edges }
  }, [p])
  const onClick: NodeMouseHandler = (_, n) => setPicked(p.arguments.find((a) => a.name === n.id) ?? null)
  const lit = useMemo(() => {
    if (!picked) return null
    const s = new Set([picked.name])
    for (const a of p.attacks) if (a.from === picked.name || a.to === picked.name) s.add(a.from).add(a.to)
    return s
  }, [picked, p])
  return (
    <>
      <div className="pm-flow" role="figure" aria-label={`Diagram of ${p.title}: what defeats what`}>
        <ReactFlow
          nodes={nodes.map((n) => (lit && !lit.has(n.id) ? { ...n, className: n.className + ' pm-dim' } : n))}
          edges={edges.map((e) =>
            lit && (e.source === picked?.name || e.target === picked?.name) ? { ...e, className: 'pm-edge pm-lit' } : e,
          )}
          onNodeClick={onClick}
          onPaneClick={() => setPicked(null)}
          fitView
          minZoom={0.2}
          nodesConnectable={false}
          proOptions={{ hideAttribution: true }}
        >
          <Background gap={24} />
          <Controls showInteractive={false} />
        </ReactFlow>
      </div>
      <p className="meta mt-2 text-xs">
        An arrow is &ldquo;defeats&rdquo;: evidence documented present defeats the criteria it points to, and a criterion that
        holds defeats a decision. A decision holds when everything pointing at it is defeated. Click any box.
      </p>
      <div aria-live="polite">
        {picked && (
          <div className="note mt-2">
            <span className={`kind ${picked.kind}`}>{picked.kind}</span> <strong>{picked.text}</strong>
            <div className="clause">Policy: “{picked.clause}”</div>
          </div>
        )}
      </div>
    </>
  )
}

/** The order of a Gray code: neighbours differ by one piece of evidence. */
function gray(bits: number): number[] {
  return Array.from({ length: 1 << bits }, (_, i) => i ^ (i >> 1))
}

/** Every combination of evidence as one coloured cell; neighbours differ by one piece of evidence. */
function Combinations({ p, m }: { p: PolicyDetail; m: DecisionMap }) {
  const n = m.evidence.length
  const rb = Math.floor(n / 2)
  const cb = n - rb
  const rows = useMemo(() => gray(rb), [rb])
  const cols = useMemo(() => gray(cb), [cb])
  const cell = Math.max(4, Math.min(28, Math.floor(640 / Math.max(rows.length, cols.length))))
  const canvas = useRef<HTMLCanvasElement>(null)
  const [at, setAt] = useState<[number, number]>([0, 0])
  const mask = (r: number, c: number) => rows[r] | (cols[c] << rb)
  const text = useMemo(() => new Map(p.arguments.map((a) => [a.name, a])), [p])

  useEffect(() => {
    const cv = canvas.current
    if (!cv) return
    const ctx = cv.getContext('2d')
    if (!ctx) return
    const css = getComputedStyle(cv)
    const colour: Record<string, string> = {}
    for (const k of Object.keys(outcomeVar)) colour[k] = css.getPropertyValue(outcomeVar[k]).trim() || '#888'
    const dpr = window.devicePixelRatio || 1
    cv.width = cols.length * cell * dpr
    cv.height = rows.length * cell * dpr
    ctx.scale(dpr, dpr)
    const gap = cell >= 10 ? 1 : 0
    for (let r = 0; r < rows.length; r++)
      for (let c = 0; c < cols.length; c++) {
        ctx.fillStyle = colour[m.outcomes[mask(r, c)]]
        ctx.globalAlpha = 0.85
        ctx.fillRect(c * cell, r * cell, cell - gap, cell - gap)
      }
    ctx.globalAlpha = 1
    ctx.strokeStyle = css.getPropertyValue('--ink').trim() || '#fff'
    ctx.lineWidth = 2
    ctx.strokeRect(at[1] * cell + 1, at[0] * cell + 1, cell - 2, cell - 2)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [m, rows, cols, cell, at])

  const sel = mask(at[0], at[1])
  const present = m.evidence.filter((_, i) => sel & (1 << i))
  const dec = m.decisions[sel] >= 0 ? text.get(m.clauses[m.decisions[sel]]) : undefined
  const counts = useMemo(() => {
    const c: Record<string, number> = {}
    for (const ch of m.outcomes) c[ch] = (c[ch] ?? 0) + 1
    return c
  }, [m])

  function key(e: KeyboardEvent) {
    const d: Record<string, [number, number]> = {
      ArrowUp: [-1, 0],
      ArrowDown: [1, 0],
      ArrowLeft: [0, -1],
      ArrowRight: [0, 1],
    }
    const v = d[e.key]
    if (!v) return
    e.preventDefault()
    setAt(([r, c]) => [
      Math.min(rows.length - 1, Math.max(0, r + v[0])),
      Math.min(cols.length - 1, Math.max(0, c + v[1])),
    ])
  }

  return (
    <div className="grid gap-5 lg:grid-cols-[auto_minmax(0,1fr)]">
      <div>
        <canvas
          ref={canvas}
          className="pm-grid"
          style={{ width: cols.length * cell, height: rows.length * cell }}
          tabIndex={0}
          role="img"
          aria-label={`All ${m.outcomes.length} combinations of evidence: ${Object.entries(counts)
            .map(([k, v]) => `${v} ${outcomeName[k].toLowerCase()}`)
            .join(', ')}. Use the arrow keys to move between combinations.`}
          onKeyDown={key}
          onClick={(e) => {
            const r = e.currentTarget.getBoundingClientRect()
            setAt([
              Math.min(rows.length - 1, Math.floor((e.clientY - r.top) / cell)),
              Math.min(cols.length - 1, Math.floor((e.clientX - r.left) / cell)),
            ])
          }}
        />
        <ul className="m-0 mt-3 flex list-none flex-wrap gap-3 p-0 text-sm">
          {Object.entries(counts).map(([k, v]) => (
            <li key={k} className="flex items-center gap-1.5">
              <span className="h-3 w-3 rounded-sm" style={{ background: `var(${outcomeVar[k]})` }} aria-hidden="true" />
              {outcomeName[k]} <span className="meta tabular-nums">{v}</span>
            </li>
          ))}
        </ul>
      </div>
      <div aria-live="polite" className="min-w-0">
        <div className="text-[11px] font-bold uppercase tracking-[0.1em] text-faint">This combination</div>
        <div className="mt-1 text-2xl font-extrabold" style={{ color: `var(${outcomeVar[m.outcomes[sel]]})` }}>
          {outcomeName[m.outcomes[sel]]}
        </div>
        {dec && (
          <p className="mt-1">
            {dec.text}
            <span className="clause block">Policy: “{dec.clause}”</span>
          </p>
        )}
        <ul className="mt-3 list-none space-y-1 p-0 text-sm">
          {m.evidence.map((e, i) => (
            <li key={e} className="flex items-start gap-2">
              <span
                className={`mt-1 h-2.5 w-2.5 flex-none rounded-full ${sel & (1 << i) ? 'bg-ok' : 'bg-line'}`}
                aria-hidden="true"
              />
              <span className={sel & (1 << i) ? '' : 'text-faint'}>
                {text.get(e)?.text ?? e}
                <span className="sr-only">{sel & (1 << i) ? ': documented present' : ': documented absent'}</span>
              </span>
            </li>
          ))}
        </ul>
        <p className="meta mt-3 text-xs">
          {present.length} of {m.evidence.length} documented present, the rest documented absent. Each step to a
          neighbouring cell changes one piece of evidence.
        </p>
      </div>
    </div>
  )
}

/** The policy map: its rules as a diagram, and every combination of evidence as a grid of outcomes. */
export function PolicyMap({ id }: { id: string }) {
  const { data: p } = usePolicyDetailQuery(id)
  const { data: m, error } = usePolicyMapQuery(id)
  const [view, setView] = useState<'diagram' | 'grid'>('diagram')
  if (!p) return null
  return (
    <div className="mt-6">
      <h3>Policy map</h3>
      <div role="tablist" aria-label="Policy map" className="mb-3 flex gap-2">
        {(
          [
            ['diagram', 'The rules'],
            ['grid', 'Every combination'],
          ] as const
        ).map(([v, label]) => (
          <button
            key={v}
            type="button"
            role="tab"
            aria-selected={view === v}
            className={`chip ${view === v ? 'on' : ''}`}
            onClick={() => setView(v)}
          >
            {label}
          </button>
        ))}
      </div>
      {view === 'diagram' ? (
        <Diagram p={p} />
      ) : m ? (
        <Combinations p={p} m={m} />
      ) : error ? (
        <p className="meta">This policy has too much evidence to map every combination.</p>
      ) : (
        <p className="meta">Deciding every combination…</p>
      )}
    </div>
  )
}
