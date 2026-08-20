// Thin fetch wrapper around the Go backend. In dev, Vite proxies /api to :8080;
// in prod the same origin serves both the SPA and the API.
async function get(path, params) {
  const qs = params
    ? '?' + new URLSearchParams(Object.entries(params).filter(([, v]) => v !== '' && v != null)).toString()
    : ''
  const res = await fetch('/api' + path + qs)
  if (!res.ok) {
    let msg = res.statusText
    try { msg = (await res.json()).error || msg } catch {}
    throw new Error(msg)
  }
  return res.json()
}

export const api = {
  meta: () => get('/meta'),
  namespaces: () => get('/namespaces'),
  changes: (params) => get('/changes', params),
  configs: (params) => get('/configs', params),
  versions: (params) => get('/versions', params),
  content: (nid) => get('/content', { nid }),
  diff: (a, b) => get('/diff', { a, b }),
}

// consoleUrl builds a deep link into the Aliyun MSE Nacos console config detail
// page (history tab). The route lives inside the hash, so region and all params
// go AFTER the '#'; param names are case-sensitive (lowercase dataId/group/
// namespaceId). Cluster-context params come from /api/meta.
export function consoleUrl(meta, row) {
  if (!meta) return null
  const p = new URLSearchParams({
    ...(meta.consoleParams || {}),
    InstanceId: meta.instanceId,
    region: meta.region,
    namespaceId: row.nsId || '',
    dataId: row.dataId,
    group: row.group || 'DEFAULT_GROUP',
    subTitle: row.dataId,
    tab: 'history',
  })
  return `https://mse.console.aliyun.com/#/Instance/Config/Detail?${p.toString()}`
}
