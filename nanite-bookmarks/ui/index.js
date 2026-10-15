import React, { useCallback, useEffect, useRef, useState } from 'react'

const base = '/api/plugins/nanite.bookmarks'
async function request(path, options = {}) {
  const response = await fetch(base + path, { ...options, headers: { 'Content-Type': 'application/json', ...options.headers } })
  const value = await response.json()
  if (!response.ok) throw new Error(value.error || 'Bookmarks unavailable')
  return value
}

// The host owns the panel frame and supplies the active canonical session ID.
export function BookmarksWidget({ session_id }) {
  const [result, setResult] = useState({ session: null, rows: [], nextPage: null })
  const active = useRef(session_id)
  active.current = session_id
  const visible = useRef({ session: session_id, count: 100 })
  if (visible.current.session !== session_id) visible.current = { session: session_id, count: 100 }
  const sequence = useRef(0)
  const rows = result.session === session_id ? result.rows : []
  const nextPage = result.session === session_id ? result.nextPage : null
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const [title, setTitle] = useState('')
  const [busy, setBusy] = useState(false)
  const refresh = useCallback(async (signal) => {
    if (!session_id || active.current !== session_id) return
    const requestSequence = ++sequence.current
    try {
      let collected = [], offset = 0, value
      do {
        value = await request('/bookmarks?session_id=' + encodeURIComponent(session_id) + '&offset=' + offset, { signal })
        collected = [...collected, ...value.bookmarks]
        offset = value.next_offset
      } while (value.more && offset < visible.current.count && active.current === session_id && !signal?.aborted)
      if (!signal?.aborted && active.current === session_id && sequence.current === requestSequence) {
        setResult({ session: session_id, rows: collected, nextPage: value.more ? value.next_offset : null }); setError('')
      }
    } catch (failure) { if (!signal?.aborted && active.current === session_id && sequence.current === requestSequence) setError(failure.message) }
  }, [session_id])
  useEffect(() => {
    setResult({ session: session_id, rows: [], nextPage: null }); setError(''); setMessage(''); setTitle(''); setBusy(false)
    const controller = new AbortController()
    let timeout
    async function poll() { await refresh(controller.signal); if (!controller.signal.aborted) timeout = setTimeout(poll, 3000) }
    void poll()
    return () => { controller.abort(); clearTimeout(timeout) }
  }, [refresh])
  async function mutate(path, method, body) {
    setBusy(true)
    try { await request(path, { method, body: body ? JSON.stringify(body) : undefined }); await refresh() }
    catch (failure) { if (active.current === session_id) setError(failure.message) }
    finally { if (active.current === session_id) setBusy(false) }
  }
  function jump(id) {
    // Compare data values directly; message IDs never become CSS selectors.
    const target = Array.from(document.querySelectorAll('[data-message-id]')).find(element => element.dataset.messageId === id)
    if (!target) { setError('The referenced message is not in this transcript. The bookmark is retained.'); return }
    target.scrollIntoView({ behavior: 'smooth', block: 'center' })
  }
  if (!session_id) return React.createElement('p', null, 'Select a session to view bookmarks.')
  return React.createElement('div', { style: { display: 'grid', gap: '0.5rem', fontSize: '0.8rem' } },
    error && React.createElement('p', { role: 'alert' }, error),
    rows.length === 0 && !error && React.createElement('p', null, 'No bookmarks yet. Save the latest reply or use /bookmark.'),
    React.createElement('button', { disabled: busy, onClick: () => void mutate('/latest', 'POST', { session_id, note: title }) }, 'Bookmark latest reply'),
    ...rows.map(row => React.createElement('div', { key: row.id, style: { display: 'flex', gap: '0.4rem', alignItems: 'center' } },
      React.createElement('button', { onClick: () => jump(row.message_id), style: { flex: 1, textAlign: 'left', overflowWrap: 'anywhere' } },
        row.note || 'Bookmarked message', React.createElement('small', { style: { display: 'block', opacity: 0.65 } }, row.created_at)),
      React.createElement('button', { disabled: busy, 'aria-label': 'Edit bookmark title', onClick: () => {
        const note = window.prompt('Bookmark title', row.note)
        if (note !== null) void mutate('/bookmarks/' + encodeURIComponent(row.id), 'PATCH', { note })
      } }, 'Edit'),
      React.createElement('button', { disabled: busy, 'aria-label': 'Delete bookmark', onClick: () => void mutate('/bookmarks/' + encodeURIComponent(row.id), 'DELETE') }, 'Delete'))),
    nextPage !== null && React.createElement('button', { disabled: busy, onClick: async () => {
      visible.current.count += 100
      await refresh()
    } }, 'Load more bookmarks'),
    React.createElement('form', { onSubmit: event => {
      event.preventDefault()
      void mutate('/bookmarks', 'POST', { session_id, message_id: message.trim(), note: title })
    }, style: { display: 'grid', gap: '0.4rem' } },
      React.createElement('input', { value: message, required: true, 'aria-label': 'Message ID', placeholder: 'Message ID', onChange: event => setMessage(event.target.value) }),
      React.createElement('input', { value: title, maxLength: 8192, 'aria-label': 'Bookmark title', placeholder: 'Title (optional)', onChange: event => setTitle(event.target.value) }),
      React.createElement('button', { type: 'submit', disabled: busy || !message.trim() }, 'Save bookmark')))
}
