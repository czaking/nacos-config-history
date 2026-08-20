import React, { useEffect, useState } from 'react'
import ChangesView from './ChangesView.jsx'
import HistoryView from './HistoryView.jsx'
import Logo from './Logo.jsx'

// Theme is persisted in localStorage; default follows the OS preference on first
// visit so the page doesn't flash the wrong palette.
function initialTheme() {
  const saved = localStorage.getItem('theme')
  if (saved === 'light' || saved === 'dark') return saved
  return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

export default function App() {
  const [tab, setTab] = useState('changes')
  const [theme, setTheme] = useState(initialTheme)
  // focus lets the changes view hand a specific config (and optional version
  // pair) to the history view when the user clicks "在历史对比中打开".
  const [focus, setFocus] = useState(null)

  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
    localStorage.setItem('theme', theme)
  }, [theme])

  function openHistory(f) {
    setFocus(f)
    setTab('history')
  }

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">
          <Logo />
          <h1>Nacos 配置变更审计</h1>
        </div>
        <nav className="tabs">
          <button className={tab === 'changes' ? 'on' : ''} onClick={() => setTab('changes')}>
            变更记录
          </button>
          <button className={tab === 'history' ? 'on' : ''} onClick={() => setTab('history')}>
            历史对比
          </button>
        </nav>
        <div className="topbar-right">
          <button
            className="theme-btn"
            onClick={() => setTheme((t) => (t === 'dark' ? 'light' : 'dark'))}
            title={theme === 'dark' ? '切换到浅色主题' : '切换到深色主题'}
          >
            {theme === 'dark' ? '☀️ 浅色' : '🌙 深色'}
          </button>
          <a
            className="feedback-btn"
            href="https://www.feishu.cn/invitation/page/add_contact/?token=6e4te008-1311-4813-9436-bdbafc28bd79&unique_id=1dDW7CG7M_XjoEKFljPZcQ=="
            target="_blank"
            rel="noreferrer"
            title="有 bug 或想法？飞书上戳我"
          >
            🐛 抓虫 &amp; 许愿池
          </a>
        </div>
      </header>
      <main>
        {tab === 'changes'
          ? <ChangesView onOpenHistory={openHistory} />
          : <HistoryView focus={focus} onConsumeFocus={() => setFocus(null)} />}
      </main>
    </div>
  )
}
