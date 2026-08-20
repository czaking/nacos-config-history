import React, { useEffect, useMemo, useState } from 'react'
import { api } from './api.js'
import { lineDiff, diffStats } from './diff.js'
import DiffBlock from './DiffBlock.jsx'

function fmtTime(ms) {
  if (!ms) return ''
  return new Date(ms).toLocaleString('zh-CN', { hour12: false })
}

const OP_LABEL = { I: '新增', U: '修改', D: '删除', LIVE: '当前' }
function opLabel(op) { return OP_LABEL[op] || op || '修改' }
const isLive = (v) => v.opType === 'LIVE'

export default function HistoryView({ focus, onConsumeFocus }) {
  const [namespaces, setNamespaces] = useState([])
  const [ns, setNs] = useState('')
  const [configs, setConfigs] = useState([])   // distinct {dataId, group}
  const [cfgKey, setCfgKey] = useState('')      // "group\x00dataId"
  const [versions, setVersions] = useState([])
  const [aNid, setANid] = useState(null)
  const [bNid, setBNid] = useState(null)
  const [diff, setDiff] = useState(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  // When arriving from "在历史对比中打开", jump straight to the target config
  // and preselect the two versions of that change.
  const [pending, setPending] = useState(null) // { group, dataId, aNid, bNid }
  useEffect(() => {
    if (!focus) return
    setNs(focus.nsId)
    setPending({ group: focus.group || 'DEFAULT_GROUP', dataId: focus.dataId, aNid: focus.aNid, bNid: focus.bNid })
    onConsumeFocus && onConsumeFocus()
  }, [focus])

  useEffect(() => { api.namespaces().then(setNamespaces).catch(() => {}) }, [])

  // When namespace changes, load its distinct config list (includes configs
  // that only have a live snapshot, not just ones that changed within history).
  useEffect(() => {
    setConfigs([]); setCfgKey(''); setVersions([]); setDiff(null)
    if (!ns) return
    api.configs({ ns }).then((rows) => {
      const list = (rows || []).map((r) => ({ dataId: r.dataId, group: r.group, key: r.group + '\x00' + r.dataId }))
      setConfigs(list.sort((a, b) => a.dataId.localeCompare(b.dataId)))
    }).catch((e) => setErr(e.message))
  }, [ns])

  // Apply a pending config selection (arrived via "在历史对比中打开") once the
  // config list for its namespace has loaded.
  useEffect(() => {
    if (!pending || !configs.length) return
    const key = (pending.group || 'DEFAULT_GROUP') + '\x00' + pending.dataId
    if (configs.some((c) => c.key === key)) setCfgKey(key)
  }, [pending, configs])

  // When config changes, load its full version timeline.
  useEffect(() => {
    setVersions([]); setANid(null); setBNid(null); setDiff(null)
    if (!ns || !cfgKey) return
    const cfg = configs.find((c) => c.key === cfgKey)
    if (!cfg) return
    api.versions({ ns, group: cfg.group, dataId: cfg.dataId }).then((rows) => {
      const vs = rows || []
      setVersions(vs)
      // If we arrived from a specific change, honor its version pair; otherwise
      // default to the two newest versions.
      if (pending && pending.dataId === cfg.dataId) {
        const b = pending.bNid || (vs[0] && vs[0].nid)
        const a = pending.aNid || (vs[1] && vs[1].nid)
        setBNid(b || null); setANid(a || null)
        setPending(null)
      } else if (vs.length >= 2) { setBNid(vs[0].nid); setANid(vs[1].nid) }
      else if (vs.length === 1) { setBNid(vs[0].nid) }
    }).catch((e) => setErr(e.message))
  }, [cfgKey])

  async function runDiff() {
    if (!aNid || !bNid) return
    setBusy(true); setErr('')
    try {
      const d = await api.diff(aNid, bNid)
      setDiff({ a: d.a, b: d.b, rows: lineDiff(d.a.content, d.b.content) })
    } catch (e) {
      setErr(e.message); setDiff(null)
    } finally {
      setBusy(false)
    }
  }
  useEffect(() => { if (aNid && bNid) runDiff() }, [aNid, bNid])

  const stats = useMemo(() => (diff ? diffStats(diff.rows) : null), [diff])

  return (
    <div className="view">
      <div className="filters">
        <label>命名空间
          <select value={ns} onChange={(e) => setNs(e.target.value)}>
            <option value="">选择…</option>
            {namespaces.map((n) => <option key={n.nsId} value={n.nsId}>{n.nsName || n.nsId}</option>)}
          </select>
        </label>
        <label>配置
          <select value={cfgKey} onChange={(e) => setCfgKey(e.target.value)} disabled={!configs.length}>
            <option value="">{configs.length ? '选择…' : '（先选命名空间）'}</option>
            {configs.map((c) => <option key={c.key} value={c.key}>{c.dataId}{c.group !== 'DEFAULT_GROUP' ? ` (${c.group})` : ''}</option>)}
          </select>
        </label>
      </div>

      {err && <div className="error">{err}</div>}

      {versions.length > 0 && (
        <div className="diffgrid">
          <div className="timeline">
            <div className="timeline-hd">版本时间线（选两个版本对比）</div>
            <table className="vtable">
              <thead><tr><th>A</th><th>B</th><th>时间</th><th>操作人</th><th>操作</th></tr></thead>
              <tbody>
                {versions.map((v) => (
                  <tr key={v.nid} className={v.nid === aNid ? 'sel-a' : v.nid === bNid ? 'sel-b' : ''}>
                    <td><input type="radio" name="a" checked={v.nid === aNid} onChange={() => setANid(v.nid)} /></td>
                    <td><input type="radio" name="b" checked={v.nid === bNid} onChange={() => setBNid(v.nid)} /></td>
                    <td className="mono">
                      {isLive(v)
                        ? <span className="dim" title={'同步于 ' + fmtTime(v.modifiedMs)}>实时值</span>
                        : fmtTime(v.modifiedMs)}
                    </td>
                    <td>{v.username || (isLive(v) ? <span className="dim">—</span> : <span className="dim">{v.srcUser}</span>)}</td>
                    <td>
                      {isLive(v)
                        ? <span className="op op-live">当前 (live)</span>
                        : <span className="dim">{opLabel(v.opType)}</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <div className="diffpane">
            {busy && <div className="dim">对比中…</div>}
            {diff && (
              <>
                <div className="diff-hd">
                  <span className="tag tag-a">A {diff.a.nid < 0 ? '当前(live)' : '#' + diff.a.nid}</span>
                  <span className="tag tag-b">B {diff.b.nid < 0 ? '当前(live)' : '#' + diff.b.nid}</span>
                  {stats && <span className="dim">+{stats.add} / -{stats.del}</span>}
                </div>
                <DiffBlock rows={diff.rows} />
              </>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
