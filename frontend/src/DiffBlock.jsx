import React, { useMemo, useState } from 'react'

// DiffBlock renders line-diff rows (from diff.js lineDiff) and, by default,
// collapses long runs of unchanged lines into a clickable "… N 行未改动 …"
// divider so the actual changes stand out. Click a divider to expand it.
const CONTEXT = 3

export default function DiffBlock({ rows }) {
  const [expanded, setExpanded] = useState(() => new Set())

  const segments = useMemo(() => buildSegments(rows), [rows])
  const hasChange = rows.some((r) => r.type !== 'same')

  return (
    <pre className="diff">
      {!hasChange && <div className="dl note-inline">两个版本内容完全一致</div>}
      {segments.map((seg, si) => {
        if (seg.type === 'gap' && !expanded.has(si)) {
          return (
            <div key={si} className="dl gap" onClick={() => setExpanded((s) => new Set(s).add(si))}>
              <span className="gap-label">⋯ {seg.rows.length} 行未改动，点击展开 ⋯</span>
            </div>
          )
        }
        return seg.rows.map((r, i) => (
          <div key={si + '-' + i} className={'dl dl-' + r.type}>
            <span className="ln">{r.aln ?? ''}</span>
            <span className="ln">{r.bln ?? ''}</span>
            <span className="sign">{r.type === 'add' ? '+' : r.type === 'del' ? '-' : ' '}</span>
            <span className="code">{r.text}</span>
          </div>
        ))
      })}
    </pre>
  )
}

// buildSegments groups rows into visible chunks (changes plus CONTEXT lines of
// surrounding context) and collapsible gaps (the unchanged middle).
function buildSegments(rows) {
  const keep = new Array(rows.length).fill(false)
  rows.forEach((r, i) => {
    if (r.type !== 'same') {
      for (let j = Math.max(0, i - CONTEXT); j <= Math.min(rows.length - 1, i + CONTEXT); j++) keep[j] = true
    }
  })
  const segs = []
  let i = 0
  while (i < rows.length) {
    const k = keep[i]
    const start = i
    while (i < rows.length && keep[i] === k) i++
    const chunk = rows.slice(start, i)
    // Don't bother collapsing a gap that's barely larger than the context.
    if (!k && chunk.length <= 2) segs.push({ type: 'ctx', rows: chunk })
    else segs.push({ type: k ? 'ctx' : 'gap', rows: chunk })
  }
  return segs
}
