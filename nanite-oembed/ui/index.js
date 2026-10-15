import React, { useState } from 'react'

function safeURL(raw) {
  try {
    const parsed = new URL(raw)
    return (parsed.protocol === 'https:' || parsed.protocol === 'http:') && !parsed.username && !parsed.password ? parsed.href : null
  } catch { return null }
}
export function OEmbedCard({ data }) {
  const [imageFailed, setImageFailed] = useState(false)
  const url = safeURL(data.url)
  const thumbnail = safeURL(data.thumbnail_url)
  const title = data.title || data.url || 'Link preview'
  return React.createElement('article', { style: { maxWidth: '28rem', border: '1px solid var(--border-subtle)', borderRadius: '0.4rem', padding: '0.75rem', overflowWrap: 'anywhere' } },
    thumbnail && !imageFailed && React.createElement('img', { src: thumbnail, alt: title, loading: 'lazy', referrerPolicy: 'no-referrer', onError: () => setImageFailed(true), style: { width: '100%', maxHeight: '16rem', objectFit: 'cover' } }),
    React.createElement('small', null, data.provider_name || 'Link'),
    React.createElement('h3', null, url ? React.createElement('a', { href: url, target: '_blank', rel: 'noopener noreferrer' }, title) : title),
    data.author_name && React.createElement('p', null, data.author_name),
    data.description && React.createElement('p', null, data.description),
    url && React.createElement('a', { href: url, target: '_blank', rel: 'noopener noreferrer' }, 'Open link'))
}
