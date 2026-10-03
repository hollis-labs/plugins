import React, {useEffect, useState} from 'react';

const h = React.createElement;
const panelStyle = {flex: 1, minHeight: 0, overflowY: 'auto', padding: 12, fontSize: 12};
const cardStyle = {border: '1px solid var(--border, #8885)', borderRadius: 8, padding: 10, marginBottom: 12};
const mono = {fontFamily: 'monospace', overflowWrap: 'anywhere'};
const text = value => value == null || value === '' ? '—' : String(value);
const num = value => typeof value === 'number' && Number.isFinite(value) ? value : 0;

// Formatting follows the pinned core TokenUsageWidget and RecentExecutionsTable.
export function formatTokens(value) {
 const n = num(value);
 if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
 if (n >= 1000) return `${(n / 1000).toFixed(1)}k`;
 return String(n);
}
export function formatUsageCost(value) {
 const n = num(value);
 if (n === 0) return '$0.00';
 if (n < 0.001) return `$${n.toFixed(4)}`;
 if (n < 0.01) return `$${n.toFixed(3)}`;
 return `$${n.toFixed(2)}`;
}
export function formatMetricCost(value) {
 const n = num(value);
 if (n === 0) return '—';
 if (n < 0.01) return `$${n.toFixed(4)}`;
 return `$${n.toFixed(2)}`;
}
export function formatDuration(value) {
 const n = num(value);
 if (n < 1000) return `${Math.round(n)}ms`;
 if (n < 60000) return `${(n / 1000).toFixed(1)}s`;
 return `${(n / 60000).toFixed(1)}m`;
}
function timestamp(value) {
 if (!value) return '—';
 const date = new Date(value);
 return Number.isNaN(date.getTime()) ? 'Unknown time' : date.toLocaleString();
}
function Rows({rows}) {
 return h('dl', {style: {display: 'grid', gridTemplateColumns: 'minmax(90px, 1fr) minmax(0, 1fr)', gap: '5px 12px'}},
  ...rows.flatMap(([label, value]) => [h('dt', {key: `${label}-label`}, label), h('dd', {key: label, style: {...mono, margin: 0}}, text(value))]));
}
function Section({title, result, children}) {
 return h('section', {style: cardStyle, 'aria-label': title}, h('h3', {style: {marginTop: 0}}, title),
  result?.loading ? h('p', {role: 'status'}, 'Loading recorded accounting…') : result?.error ? h('p', {role: 'alert'}, text(result.error)) : result?.data ? children(result.data) : h('p', null, 'Accounting data unavailable'));
}

function Usage({data}) {
 const input = num(data.input_tokens), tools = num(data.tool_input_tokens);
 return h(React.Fragment, null,
  h(Rows, {rows: [
   ['Input', formatTokens(input)], ['Content input', formatTokens(Math.max(0, input - tools))], ['Tool input', formatTokens(tools)],
   ['Output', formatTokens(data.output_tokens)], ['Total', formatTokens(data.total_tokens)],
   ['Cache creation', formatTokens(data.cache_creation_tokens)], ['Cache read', formatTokens(data.cache_read_tokens)],
   ['Recorded estimated cost', formatUsageCost(data.estimated_cost_usd)], ['Messages', data.message_count],
  ]}),
  h('p', null, 'Recorded session totals. Accounting completeness details are unavailable through this projection. Zero does not establish complete capture.'));
}

function Slots({data}) {
 if (!data.available) return h('p', null, 'Captured slot accounting unavailable. The inspector may be disabled or no capture may exist.');
 return h(React.Fragment, null,
  h('p', null, `Latest captured turn: ${text(data.turn_id)} · ${timestamp(data.started_at)}`),
  h('p', null, 'Captured accounting only; this may be an in-flight record. No prompt text is included.'),
  data.slots?.length ? data.slots.map((slot, index) => h('div', {key: `${slot.name}-${index}`, style: {...cardStyle, marginBottom: 6}},
   h('strong', null, text(slot.name)),
   h(Rows, {rows: [['Tokens', formatTokens(slot.tokens)], ['Cache', slot.cached ? 'cached' : 'not cached'],
    ['Traffic light', ['green', 'yellow', 'red'].includes(slot.traffic_light) ? slot.traffic_light : 'unknown'],
    ['Sensitive', slot.sensitive ? 'yes' : 'no'], ...(slot.cache_key ? [['Cache key', slot.cache_key]] : [])]}))) : h('p', null, 'Capture exists; no slots recorded.'));
}

const columns = [['created_at', 'Time'], ['provider', 'Provider'], ['adapter', 'Adapter'], ['model', 'Model'],
 ['duration_ms', 'Duration'], ['input_tokens', 'In'], ['output_tokens', 'Out'], ['estimated_cost_usd', 'Recorded estimated cost']];
const sortFields = new Set(['created_at', 'duration_ms', 'input_tokens', 'output_tokens', 'estimated_cost_usd']);
function metricCell(row, field) {
 if (field === 'created_at') return timestamp(row[field]);
 if (field === 'duration_ms') return formatDuration(row[field]);
 if (field === 'estimated_cost_usd') return formatMetricCost(row[field]);
 if (field.endsWith('_tokens')) return formatTokens(row[field]);
 return text(row[field]);
}
function Metrics({data, sort, onSort, expanded, onExpand}) {
 const rows = [...(data.metrics ?? [])];
 if (sort.field) rows.sort((a, b) => {
  const av = a[sort.field], bv = b[sort.field];
  const comparison = typeof av === 'string' ? av.localeCompare(String(bv)) : num(av) - num(bv);
  return (sort.asc ? comparison : -comparison) || num(b.id) - num(a.id);
 });
 return h(React.Fragment, null,
  h('p', null, `${rows.length} recent execution metrics returned for this session (limit 50).`),
  data.more && h('p', {role: 'status'}, 'More execution metrics exist. This bounded list is not complete history; sorting applies only to these rows.'),
  rows.length ? h('div', {style: {overflowX: 'auto'}}, h('table', {style: {width: '100%', borderCollapse: 'collapse'}},
   h('thead', null, h('tr', null, ...columns.map(([field, label]) => h('th', {key: field, scope: 'col', style: {textAlign: 'left', padding: 6}, ...(sortFields.has(field) ? {'aria-sort': sort.field === field ? sort.asc ? 'ascending' : 'descending' : 'none'} : {})},
    sortFields.has(field) ? h('button', {type: 'button', onClick: () => onSort({field, asc: sort.field === field ? !sort.asc : false}), 'aria-label': `Sort recent metrics by ${label}`}, label) : label)), h('th', {scope: 'col'}, 'Status'), h('th', {scope: 'col'}, 'Details'))),
   h('tbody', null, ...rows.map(row => h(React.Fragment, {key: row.id},
    h('tr', null, ...columns.map(([field]) => h('td', {key: field, style: {padding: 6, whiteSpace: 'nowrap'}}, metricCell(row, field))),
     h('td', null, row.failed ? 'Failed' : 'No error recorded'),
     h('td', null, h('button', {type: 'button', 'aria-label': `Execution ${row.id} details`, 'aria-expanded': expanded.has(row.id), onClick: () => onExpand(row.id)}, expanded.has(row.id) ? 'Hide' : 'Show'))),
    expanded.has(row.id) && h('tr', null, h('td', {colSpan: 10, style: {padding: 10}}, h(Rows, {rows: [
     ['Execution ID', row.id], ['Session', row.session_id], ['Message reference', row.message_id], ['Agent ID', row.agent_id], ['Agent slug', row.agent_slug],
     ['Recorded mode', row.mode], ['Context messages', row.context_messages], ['Context tokens', formatTokens(row.context_tokens)],
     ['Cache creation', formatTokens(row.cache_creation_tokens)], ['Cache read', formatTokens(row.cache_read_tokens)],
     ['Tool iterations', row.tool_iterations], ['Tool calls', row.tool_calls], ['Utility call', row.is_utility ? 'yes' : 'no'],
     ['Stop reason', row.stop_reason], ['Profile name', row.profile_name], ['Profile digest', row.profile_digest],
    ]})))))))) : h('p', null, 'No execution metrics recorded yet.'));
}

const resourceNames = ['usage', 'execution_metrics', 'context_slots'];
export function DiagnosticsPanel({session_id: sessionId}) {
 const [state, setState] = useState({session: null, data: null});
 const [revision, setRevision] = useState(0);
 const [sort, setSort] = useState({field: null, asc: false});
 const [expanded, setExpanded] = useState(new Set());
 useEffect(() => {setSort({field: null, asc: false}); setExpanded(new Set());}, [sessionId]);
 useEffect(() => {
  if (!sessionId) {setState({session: null, data: null}); return;}
  const controller = new AbortController(); let live = true;
  setState({session: sessionId, data: Object.fromEntries(resourceNames.map(resource => [resource, {loading: true}]))});
  const load = async resource => {
   let result;
   try {
    const response = await fetch(`/api/plugins/nanite.diagnostics/diagnostics?${new URLSearchParams({session_id: sessionId, resource})}`, {signal: controller.signal, cache: 'no-store'});
    if (!response.ok) throw new Error(await response.text() || 'Diagnostics could not be read');
    const data = await response.json();
    if (data.session_id !== sessionId || data.resource !== resource || !data.result || (!data.result.data && !data.result.error)) throw new Error('Invalid diagnostics response');
    result = {...data.result, refreshed: new Date().toLocaleTimeString()};
   } catch (error) {
    result = {error: error.name === 'AbortError' ? 'Diagnostics read canceled' : error.message, data: null};
   }
   if (live) setState(old => ({session: sessionId, data: {...old.data, [resource]: result}}));
  };
  void resourceNames.map(load);
  return () => {live = false; controller.abort();};
 }, [sessionId, revision]);
 const current = state.session === sessionId;
 const data = current ? state.data : null;
 const toggle = id => setExpanded(old => {const next = new Set(old); next.has(id) ? next.delete(id) : next.add(id); return next;});
 const section = (title, resource, children) => h(Section, {title, result: data?.[resource] ?? {loading: true}}, value => h(React.Fragment, null,
  children(value), h('p', null, `Last read: ${data[resource].refreshed}. Refresh manually to read newer records.`)));
 return h('div', {style: panelStyle},
  h('p', null, 'Recorded accounting for the selected session. Recent metrics and latest captured slots are independent projections, not full snapshots.'),
  !sessionId ? h('p', null, 'Select a session to view recorded diagnostics.') : h(React.Fragment, null,
   h('p', {style: mono}, `Session: ${sessionId}`),
   h('button', {type: 'button', disabled: data && resourceNames.some(resource => data[resource]?.loading), onClick: () => setRevision(value => value + 1)}, 'Refresh'),
   section('Recorded session usage', 'usage', usage => h(Usage, {data: usage})),
   section('Recent execution metrics', 'execution_metrics', metrics => h(Metrics, {data: metrics, sort, onSort: setSort, expanded, onExpand: toggle})),
   section('Latest captured slot accounting', 'context_slots', slots => h(Slots, {data: slots}))));
}

export function SystemPromptsViewer() {
 return h('div', {style: panelStyle}, h('h2', null, 'STATIC Prompt References'),
  h('p', null, 'Static reference text from the source revision below. It goes stale by design until a later refresh. These are not live or effective prompts, and do not describe every assembled instruction.'),
  ...references.map(entry => h('article', {key: entry.slug, style: cardStyle},
   h('h3', null, `${entry.name} · STATIC`),
   h('p', {style: mono}, `Source: ${entry.source}`), h('p', {style: mono}, `SHA: ${entry.sha}`),
   h('details', null, h('summary', null, `Read ${entry.name} static text`),
    h('pre', {style: {...mono, whiteSpace: 'pre-wrap', maxHeight: 400, overflow: 'auto'}}, entry.text)))));
}

// Static reference snapshot, reconciled from authored source at release time.
const references = [
  {
    "slug": "default",
    "name": "Default agent",
    "source": "internal/agent/builtin/profiles/default.md",
    "sha": "f4df26f203dae4bb5c812c75226ee7abcb1dc266",
    "text": "You are a helpful AI assistant embedded in the Nanite chat harness. You have\naccess to tools \u2014 file system, HTTP, math, MCP servers, and Nanite's own\nself-tools. Your job is to use them to help the user.\n\n## Capability\n\n- You have **meta-tools** for discovery (`tool_describe`), pre-flight\n  validation (`tool_validate`), and learning capture (`lesson_capture`).\n  Reach for them when a tool's contract is unfamiliar or after a call fails \u2014\n  you don't have to memorize every schema.\n- For multi-step flows like rendering envelope cards, you don't need to own\n  the recipe \u2014 describe the intent and the harness routes you to a\n  specialized executor.\n\n## Style\n\n- Be direct. Match the user's terseness \u2014 no ceremony, no trailing summaries.\n- Use Markdown when it earns its keep (lists, code, tables). Prose otherwise.\n- Never emit an announcement or promissory preamble before calling tools (e.g. \"I will inspect...\", \"Let me check...\"). If an action or tool call is required, invoke the tool immediately in the current turn without preliminary narration.\n\n## Judgment\n\n- Ask one pointed question before a long tool chain when the scope is unclear.\n"
  },
  {
    "slug": "worker",
    "name": "Worker agent",
    "source": "internal/agent/builtin/profiles/worker.md",
    "sha": "f4df26f203dae4bb5c812c75226ee7abcb1dc266",
    "text": "You are a Worker agent in the Nanite harness, dispatched by a parent agent to handle a specific scoped task \u2014 writing code, running tools, or completing well-bounded work. You have full tool access.\n\nStay within the assigned scope. Do not initiate new conversations or expand the task beyond what the parent dispatched. When the task is done, return the result. When you cannot complete it with the tools and paths available, return an explicit failure \u2014 the universal Refusal rules govern this (your reply is treated as authoritative by the parent).\n"
  }
];
