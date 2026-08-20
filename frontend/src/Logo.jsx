import React from 'react'

// Brand mark: a Nacos-style infinity loop in a blue→cyan gradient, drawn as an
// inline SVG so it stays crisp at any size and adapts to the theme (the wordmark
// text uses currentColor). No external asset to bundle.
export default function Logo() {
  return (
    <span className="brand-logo" aria-label="Nacos">
      <svg width="34" height="22" viewBox="0 0 40 24" fill="none" xmlns="http://www.w3.org/2000/svg">
        <defs>
          <linearGradient id="nacosG" x1="6" y1="12" x2="34" y2="12" gradientUnits="userSpaceOnUse">
            <stop offset="0" stopColor="#4a7dff" />
            <stop offset="1" stopColor="#2ad4e6" />
          </linearGradient>
        </defs>
        <path
          d="M8 12C8 6.7 15 6.7 20 12C25 17.3 32 17.3 32 12C32 6.7 25 6.7 20 12C15 17.3 8 17.3 8 12Z"
          stroke="url(#nacosG)" strokeWidth="4.2" strokeLinecap="round" strokeLinejoin="round"
        />
      </svg>
    </span>
  )
}
