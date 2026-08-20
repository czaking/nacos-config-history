// Minimal line-level diff via longest-common-subsequence — no dependencies.
// Returns an array of { type: 'same'|'add'|'del', text, aln, bln } rows.
export function lineDiff(a, b) {
  const A = (a || '').split('\n')
  const B = (b || '').split('\n')
  const n = A.length
  const m = B.length

  // LCS length table.
  const dp = Array.from({ length: n + 1 }, () => new Int32Array(m + 1))
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      dp[i][j] = A[i] === B[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1])
    }
  }

  const rows = []
  let i = 0, j = 0
  while (i < n && j < m) {
    if (A[i] === B[j]) {
      rows.push({ type: 'same', text: A[i], aln: i + 1, bln: j + 1 })
      i++; j++
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      rows.push({ type: 'del', text: A[i], aln: i + 1, bln: null })
      i++
    } else {
      rows.push({ type: 'add', text: B[j], aln: null, bln: j + 1 })
      j++
    }
  }
  while (i < n) rows.push({ type: 'del', text: A[i], aln: ++i, bln: null })
  while (j < m) rows.push({ type: 'add', text: B[j], aln: null, bln: ++j })
  return rows
}

export function diffStats(rows) {
  let add = 0, del = 0
  for (const r of rows) {
    if (r.type === 'add') add++
    else if (r.type === 'del') del++
  }
  return { add, del }
}
