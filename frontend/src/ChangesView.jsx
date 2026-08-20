import React, { useEffect, useMemo, useState } from 'react'
import { api, consoleUrl } from './api.js'
import { lineDiff, diffStats } from './diff.js'
import DiffBlock from './DiffBlock.jsx'

function fmtTime(ms) {
  if (!ms) return ''
  return new Date(ms).toLocaleString('zh-CN', { hour12: false })
}

const OP_LABEL = { I: '新增', U: '修改', D: '删除', LIVE: '当前' }
function opLabel(op) { return OP_LABEL[op] || op || '修改' }

// Production namespaces, by name. Listed explicitly (rather than matching "prod"
// in the name) so namespaces that are prod but not named that way are still
// included, and dev/staging namespaces that happen to contain the substring are
// not misclassified. Adjust this list to match your own namespaces.
const PROD_NS = new Set([
  'app-prod', 'app-prod-k8s', 'biz_prod',
  'web-prod', 'strategy-prod', 'api-prod', 'client-config',
])

function todayStr() {
  const d = new Date()
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

function download(filename, text, mime) {
  const blob = new Blob([text], { type: mime + ';charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url; a.download = filename
  document.body.appendChild(a); a.click(); a.remove()
  URL.revokeObjectURL(url)
}

const EXPORT_COLS = ['时间', '操作人', '操作', '命名空间', '配置(DataId)', 'Group', '来源IP']
function rowCells(r) {
  return [fmtTime(r.modifiedMs), r.username || r.srcUser, opLabel(r.opType), r.nsName, r.dataId, r.group, r.srcIp]
}
function csvEscape(v) {
  const s = String(v ?? '')
  return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s
}
function toCSV(rows) {
  const lines = [EXPORT_COLS.join(',')]
  for (const r of rows) lines.push(rowCells(r).map(csvEscape).join(','))
  return '﻿' + lines.join('\n') // BOM so Excel reads UTF-8
}
function toMarkdown(rows, title) {
  const out = [`# ${title}`, '', `共 ${rows.length} 条变更`, '',
    '| ' + EXPORT_COLS.join(' | ') + ' |',
    '| ' + EXPORT_COLS.map(() => '---').join(' | ') + ' |']
  for (const r of rows) out.push('| ' + rowCells(r).map((c) => String(c ?? '').replace(/\|/g, '\\|')).join(' | ') + ' |')
  return out.join('\n')
}

export default function ChangesView({ onOpenHistory }) {
  const [namespaces, setNamespaces] = useState([])
  const [meta, setMeta] = useState(null)
  const [date, setDate] = useState(todayStr())
  const [dateEnd, setDateEnd] = useState('') // optional range end; empty = single day
  const [ns, setNs] = useState('')
  const [env, setEnv] = useState('all') // all | prod | nonprod (applied when no single ns chosen)
  const [filter, setFilter] = useState('')
  const [humanOnly, setHumanOnly] = useState(true) // hide automated/unmapped accounts by default
  const [rows, setRows] = useState([])
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')
  const [page, setPage] = useState(1) // 1-based; client-side pagination over the filtered rows

  const PAGE_SIZE = 20

  // Inline "what did this change do" modal: the clicked version vs its predecessor.
  const [detail, setDetail] = useState(null) // { row, prev, rows } | { row, error } | { row, loading:true }

  async function viewChange(row) {
    setDetail({ row, loading: true })
    try {
      const vs = await api.versions({ ns: row.nsId, group: row.group, dataId: row.dataId })
      const idx = (vs || []).findIndex((v) => v.nid === row.nid)
      const prev = idx >= 0 ? vs[idx + 1] : null // list is newest-first; +1 is older
      const cur = await api.content(row.nid)
      const prevContent = prev ? (await api.content(prev.nid)).content : ''
      setDetail({ row, prev, rows: lineDiff(prevContent, cur.content) })
    } catch (e) {
      setDetail({ row, error: e.message })
    }
  }

  useEffect(() => {
    api.namespaces().then(setNamespaces).catch(() => {})
    api.meta().then(setMeta).catch(() => {})
  }, [])

  async function load() {
    // Wait until the namespace list is available for the multi-namespace fetch.
    if (!ns && namespaces.length === 0) return
    setLoading(true); setErr('')
    try {
      let data
      if (ns) {
        data = (await api.changes({ date, dateEnd, ns, limit: 2000 })) || []
      } else {
        // Fetch each namespace separately and merge. A single all-namespaces
        // query capped at N rows lets a high-churn namespace (e.g. the
        // automation bot writing thousands/day) crowd out other
        // namespaces' changes; per-namespace fetching prevents that.
        const want = namespaces.filter((n) =>
          env === 'prod' ? PROD_NS.has(n.nsName)
          : env === 'nonprod' ? !PROD_NS.has(n.nsName)
          : true)
        const parts = await Promise.all(
          want.map((n) => api.changes({ date, dateEnd, ns: n.nsId, limit: 2000 }).then((r) => r || []).catch(() => []))
        )
        data = parts.flat().sort((a, b) => b.modifiedMs - a.modifiedMs)
      }
      setRows(data)
    } catch (e) {
      setErr(e.message); setRows([])
    } finally {
      setLoading(false)
    }
  }
  useEffect(() => { load() }, [date, dateEnd, ns, env, namespaces])

  const shown = useMemo(() => {
    let list = rows
    // Namespace/environment scoping happens at fetch time (see load()).
    // "只看人工改动": drop operators that didn't resolve to a real person
    // (service/automation accounts like an automation-account writer).
    if (humanOnly) list = list.filter((r) => (r.username || '').trim() !== '')
    const f = filter.trim().toLowerCase()
    if (!f) return list
    return list.filter((r) =>
      [r.username, r.srcUser, r.dataId, r.nsName, r.group].some((v) => (v || '').toLowerCase().includes(f))
    )
  }, [rows, filter, humanOnly])

  // Client-side pagination over the filtered set (20/page). byUser summary and
  // exports still operate on the full `shown` set, not just the current page.
  useEffect(() => { setPage(1) }, [rows, filter, humanOnly])
  const totalPages = Math.max(1, Math.ceil(shown.length / PAGE_SIZE))
  const curPage = Math.min(page, totalPages)
  const paged = useMemo(
    () => shown.slice((curPage - 1) * PAGE_SIZE, curPage * PAGE_SIZE),
    [shown, curPage]
  )

  // Per-person summary: who changed how many configs today.
  const byUser = useMemo(() => {
    const m = new Map()
    for (const r of shown) {
      const key = r.username || r.srcUser || '(未知)'
      if (!m.has(key)) m.set(key, new Set())
      m.get(key).add(r.nsName + '/' + r.dataId)
    }
    return [...m.entries()].map(([user, set]) => ({ user, configs: set.size })).sort((a, b) => b.configs - a.configs)
  }, [shown])

  // Build a descriptive scope label for exports from the active filters.
  function scopeLabel() {
    if (ns) {
      const n = namespaces.find((x) => x.nsId === ns)
      return n ? (n.nsName || n.nsId) : ns
    }
    if (env === 'prod') return 'prod'
    if (env === 'nonprod') return 'nonprod'
    return '全部'
  }
  function exportBase() {
    return `nacos变更_${date || '全部日期'}_${scopeLabel()}${humanOnly ? '_人工' : ''}`
  }
  function exportTitle() {
    return `Nacos 配置变更 · ${date || '全部日期'} · ${scopeLabel()}${humanOnly ? ' · 仅人工改动' : ''}`
  }

  return (
    <div className="view">
      <div className="filters">
        <label>日期
          <input type="date" value={date} onChange={(e) => setDate(e.target.value)} />
        </label>
        <label>至（可选）
          <input type="date" value={dateEnd} min={date}
            onChange={(e) => setDateEnd(e.target.value)}
            title="留空则只看单天；填了则查 [起始日, 结束日] 区间" />
        </label>
        <label>命名空间
          <select value={ns} onChange={(e) => setNs(e.target.value)}>
            <option value="">全部</option>
            {namespaces.map((n) => (
              <option key={n.nsId} value={n.nsId}>{n.nsName || n.nsId}</option>
            ))}
          </select>
        </label>
        <label>环境
          <select value={env} onChange={(e) => setEnv(e.target.value)} disabled={!!ns}
            title={ns ? '已选具体命名空间，环境过滤忽略' : ''}>
            <option value="all">全部环境</option>
            <option value="prod">仅生产 prod</option>
            <option value="nonprod">仅非生产</option>
          </select>
        </label>
        <label>搜索
          <input type="text" placeholder="操作人 / 配置 / 命名空间" value={filter}
            onChange={(e) => setFilter(e.target.value)} />
        </label>
        <label className="check">
          <input type="checkbox" checked={humanOnly} onChange={(e) => setHumanOnly(e.target.checked)} />
          只看人工改动
        </label>
        <button onClick={load} disabled={loading}>{loading ? '加载中…' : '刷新'}</button>
      </div>

      {err && <div className="error">加载失败：{err}</div>}

      {byUser.length > 0 && (
        <div className="summary">
          {byUser.map((u) => (
            <span className="chip" key={u.user}>{u.user} · {u.configs} 项</span>
          ))}
        </div>
      )}

      <div className="tablewrap">
        <table>
          <thead>
            <tr>
              <th>时间</th><th>操作人</th><th>操作</th><th>命名空间</th>
              <th>配置 (DataId)</th><th>Group</th><th>来源 IP</th><th></th>
            </tr>
          </thead>
          <tbody>
            {paged.map((r) => (
              <tr key={r.nid}>
                <td className="mono">{fmtTime(r.modifiedMs)}</td>
                <td>{r.username || <span className="dim">{r.srcUser}</span>}</td>
                <td><span className={'op op-' + (r.opType || 'U')}>{opLabel(r.opType)}</span></td>
                <td>{r.nsName}</td>
                <td className="mono">{r.dataId}</td>
                <td className="dim">{r.group}</td>
                <td className="mono dim">{r.srcIp}</td>
                <td><button className="link" onClick={() => viewChange(r)}>查看改动</button></td>
              </tr>
            ))}
            {!loading && shown.length === 0 && (
              <tr><td colSpan="8" className="empty">当天无变更记录</td></tr>
            )}
          </tbody>
        </table>
      </div>
      <div className="count">
        <span>{shown.length} 条变更</span>
        {totalPages > 1 && (
          <span className="pager">
            <button className="link" disabled={curPage <= 1} onClick={() => setPage(curPage - 1)}>← 上一页</button>
            <span className="dim">第 {curPage} / {totalPages} 页</span>
            <button className="link" disabled={curPage >= totalPages} onClick={() => setPage(curPage + 1)}>下一页 →</button>
          </span>
        )}
        {shown.length > 0 && (
          <label className="export">
            导出
            <select value="" onChange={(e) => {
              const v = e.target.value
              if (v === 'csv') download(exportBase() + '.csv', toCSV(shown), 'text/csv')
              else if (v === 'md') download(exportBase() + '.md', toMarkdown(shown, exportTitle()), 'text/markdown')
              e.target.value = ''
            }}>
              <option value="">选择格式…</option>
              <option value="csv">CSV（Excel）</option>
              <option value="md">Markdown</option>
            </select>
          </label>
        )}
      </div>

      {detail && (
        <ChangeDetail
          detail={detail}
          meta={meta}
          onClose={() => setDetail(null)}
          onOpenHistory={() => {
            const r = detail.row
            onOpenHistory && onOpenHistory({
              nsId: r.nsId, group: r.group, dataId: r.dataId,
              aNid: detail.prev ? detail.prev.nid : null, bNid: r.nid,
            })
            setDetail(null)
          }}
        />
      )}
    </div>
  )
}

// ChangeDetail shows exactly what one change modified: the clicked version diffed
// against its immediate predecessor (green added / red removed).
function ChangeDetail({ detail, meta, onClose, onOpenHistory }) {
  const { row } = detail
  const stats = detail.rows ? diffStats(detail.rows) : null
  const href = consoleUrl(meta, row)
  return (
    <div className="modal-mask" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <div className="modal-hd">
          <div>
            <strong>{row.dataId}</strong>
            <span className="dim"> · {row.nsName} · {fmtTime(row.modifiedMs)} · {row.username || row.srcUser}</span>
          </div>
          <div className="modal-hd-right">
            {stats && <span className="dim">+{stats.add} / -{stats.del}</span>}
            <button className="link" onClick={onOpenHistory}>在历史对比中打开 →</button>
            {href && <a className="link" href={href} target="_blank" rel="noreferrer">去 Nacos 控制台 ↗</a>}
            <button className="link" onClick={onClose}>关闭 ✕</button>
          </div>
        </div>
        <div className="modal-body">
          {detail.loading && <div className="dim note">加载中…</div>}
          {detail.error && <div className="error">{detail.error}</div>}
          {detail.rows && !detail.prev && (
            <div className="dim note">这是该配置的最早版本，下面展示的是首次写入的完整内容。</div>
          )}
          {detail.rows && <DiffBlock rows={detail.rows} />}
        </div>
      </div>
    </div>
  )
}
