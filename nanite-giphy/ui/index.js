import React, { useState } from 'react'

function gifURL(raw) {
  try {
    const parsed = new URL(raw)
    return parsed.protocol === 'https:' && !parsed.username && !parsed.password && (parsed.hostname === 'giphy.com' || parsed.hostname.endsWith('.giphy.com')) ? parsed.href : null
  } catch { return null }
}
export function GiphyModalCard({ data }) {
  const [loaded, setLoaded] = useState(false)
  const [failed, setFailed] = useState(false)
  const image = gifURL(data.gif_url)
  return React.createElement('figure', { style: { maxWidth: '24rem', border: '1px solid var(--border-subtle)', borderRadius: '0.4rem', padding: '0.75rem' } },
    React.createElement('figcaption', null, data.title),
    image && !failed && React.createElement('img', { src: image, alt: data.query, loading: 'lazy', referrerPolicy: 'no-referrer', onLoad: () => setLoaded(true), onError: () => setFailed(true), style: { width: '100%', display: 'block', borderRadius: '0.3rem' } }),
    (!loaded || failed || !image) && React.createElement('p', { role: failed || !image ? 'status' : undefined }, failed || !image ? 'GIF unavailable' : 'Loading GIF…'),
    React.createElement('small', null, 'Powered by ' + (data.source || 'GIPHY')))
}
