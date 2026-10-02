import React, {useEffect, useRef, useState} from 'react';
const h = React.createElement;
const base = '/api/plugins/nanite.plan';
const styles = {flex: 1, minHeight: 0, overflowY: 'auto', padding: 12};
const encode = encodeURIComponent;

export function PlanPanel({session_id: sessionId}) {
 const lifetime = useRef({session: sessionId, epoch: 0});
 if (lifetime.current.session !== sessionId) lifetime.current = {session: sessionId, epoch: lifetime.current.epoch + 1};
 const [data, setData] = useState({session: null, todos: [], plans: [], project: ''});
 const [filter, setFilter] = useState('session'), [revision, setRevision] = useState(0);
 const [busy, setBusy] = useState(false), [error, setError] = useState('');
 const [title, setTitle] = useState(''), [kind, setKind] = useState('todo'), [scope, setScope] = useState('session');
 const [todoEdit, setTodoEdit] = useState(null), [planEdit, setPlanEdit] = useState(null);
 useEffect(() => {
  setData({session: null, todos: [], plans: [], project: ''}); setError(''); setBusy(false); setTodoEdit(null); setPlanEdit(null); setTitle('');
 }, [sessionId]);
 useEffect(() => {
  const controller = new AbortController(); let active = true, loading = false;
  const read = async (resource, parameters) => {
   const rows = []; let offset = 0, reply;
   do {
    const query = new URLSearchParams({session_id: sessionId, ...parameters, offset: String(offset)});
    const response = await fetch(`${base}/${resource}?${query}`, {signal: controller.signal});
    if (!response.ok) throw new Error(await response.text() || 'Could not load work');
    reply = await response.json(); rows.push(...reply[resource]);
    if (reply.more && reply.next_offset <= offset) throw new Error('Could not continue loading work');
    offset = reply.next_offset;
   } while (active && reply.more);
   return {rows, project: reply.project_id};
  };
  const load = async () => {
   if (!sessionId || loading) return; loading = true;
   try {
    const sessionTodos = await read('todos', {scope: 'session', scope_id: sessionId});
    const project = sessionTodos.project;
    const [turnTodos, sessionPlans, projectTodos, projectPlans, workspacePlans] = await Promise.all([
     read('todos', {scope: 'turn', scope_id: sessionId}),
     read('plans', {scope: 'session', scope_id: sessionId}),
     project ? read('todos', {scope: 'project', scope_id: project}) : {rows: []},
     project ? read('plans', {scope: 'project', scope_id: project}) : {rows: []},
     read('plans', {scope: 'workspace'}),
    ]);
    if (active) {setData({session: sessionId, project, todos: [...sessionTodos.rows, ...turnTodos.rows, ...projectTodos.rows], plans: [...sessionPlans.rows, ...projectPlans.rows, ...workspacePlans.rows]}); setError('');}
   } catch (e) {if (active && e.name !== 'AbortError') setError(e.message);} finally {loading = false;}
  };
  void load(); const timer = sessionId ? setInterval(load, 30000) : null;
  const visible = () => {if (document.visibilityState === 'visible') void load();};
  window.addEventListener('focus', load); document.addEventListener('visibilitychange', visible);
  return () => {active = false; controller.abort(); if (timer) clearInterval(timer); window.removeEventListener('focus', load); document.removeEventListener('visibilitychange', visible);};
 }, [sessionId, revision]);
 const current = data.session === sessionId;
 const mutationLock = useRef(false);
 const mutate = async (path, method, body, success) => {
  if (!sessionId || !current || mutationLock.current) return;
  const epoch = lifetime.current.epoch; const live = () => epoch === lifetime.current.epoch;
  mutationLock.current = true; setBusy(true); setError('');
  try {
   const response = await fetch(`${base}${path}?session_id=${encode(sessionId)}`, {method, headers: body ? {'Content-Type': 'application/json'} : undefined, body: body ? JSON.stringify(body) : undefined});
   if (!response.ok) throw new Error(await response.text() || 'Work change failed');
   if (live()) {success?.(); setRevision(v => v + 1);}
  } catch (e) {if (live()) setError(e.message);} finally {mutationLock.current = false; if (live()) setBusy(false);}
 };
 const shown = row => filter === 'all' ? row.scope !== 'workspace' : filter === 'session' ? ['session', 'turn'].includes(row.scope) : row.scope === filter;
 const todos = current ? data.todos.filter(shown).sort((a,b) => (typeof a.metadata?.sort_order === 'number' ? a.metadata.sort_order : Infinity) - (typeof b.metadata?.sort_order === 'number' ? b.metadata.sort_order : Infinity) || new Date(a.created_at).getTime() - new Date(b.created_at).getTime()) : [];
 const plans = current ? data.plans.filter(shown) : [];
 const field = (label, value, change, props = {}) => h('label', null, label, h('input', {...props, value, onChange: e => change(e.target.value)}));
 const select = (label, value, change, options) => h('label', null, label, h('select', {value, onChange: e => change(e.target.value)}, ...options.map(([id,label,disabled]) => h('option', {key: id, value: id, disabled}, label))));
 const button = (label, action, disabled = false, accessible = label) => h('button', {'aria-label': accessible, type: 'button', disabled: busy || !current || disabled, onClick: action}, label);
 const scopes = [['session','Session'],['project','Project',!data.project],...(kind === 'plan' ? [['workspace','Workspace']] : [['turn','Turn']])];
 const reorder = (row, direction) => {
  const group = todos.filter(t => t.scope === row.scope && t.scope_id === row.scope_id);
  const index = group.findIndex(t => t.id === row.id), next = index + direction;
  if (next < 0 || next >= group.length) return;
  [group[index], group[next]] = [group[next], group[index]];
  // One batch avoids partially committed ordering and preserves other metadata.
  void mutate('/work/reorder','POST',{items: group.map((t,i) => ({id:t.id,sort_order:i}))});
 };
 if (!sessionId) return h('p', null, 'Select a session to view your plan.');
 return h('section', {'aria-label': 'Plan', style: styles},
  error && h('p', {role: 'alert'}, error),
  select('View', filter, setFilter, [['all','All'],['session','Session'],['project','Project'],['workspace','Workspace plans']]),
  h('form', {onSubmit: e => {e.preventDefault(); if (!title.trim()) return; void mutate(kind === 'todo' ? '/todos' : '/plans', 'POST', {title: title.trim(), scope, scope_id: scope === 'project' ? data.project : scope === 'workspace' ? '' : sessionId}, () => setTitle(''));}},
   select('Add', kind, v => {setKind(v);setScope('session');}, [['todo','Todo'],['plan','Plan']]),
   field('Title', title, setTitle, {required: true, maxLength: 65536}), select('Scope', scope, setScope, scopes),
   h('button', {type: 'submit', disabled: busy || !current || (scope === 'project' && !data.project)}, 'Add work')),
  h('h3', null, 'Todos'), todos.length === 0 && h('p', null, 'No todos.'),
  h('ul', null, ...todos.map(row => h('li', {key: row.id},
   h('label', null, h('input', {type: 'checkbox', checked: row.status === 'done', disabled: busy, onChange: e => void mutate(`/todos/${encode(row.id)}`, 'PUT', {status: e.target.checked ? 'done' : 'pending'})}), row.title),
   h('small', null, ` ${row.scope} · ${row.priority}${row.parent_id ? ' · child todo' : ''}`), row.description && h('p', null, row.description),
   button('Edit todo', () => setTodoEdit({...row, labelsText: JSON.stringify(row.labels ?? [])}), false, `Edit todo ${row.title}`),
   button('Delete todo', () => {if (window.confirm('Delete this todo and its children?')) void mutate(`/todos/${encode(row.id)}`, 'DELETE');},false,`Delete todo ${row.title}`),
   button(row.scope === 'project' ? 'Make session' : 'Make project', () => void mutate(`/todos/${encode(row.id)}/scope`, 'PATCH', {scope: row.scope === 'project' ? 'session' : 'project', scope_id: row.scope === 'project' ? sessionId : data.project, project_id: row.scope === 'project' ? '' : data.project}), row.scope !== 'project' && !data.project, `${row.scope === 'project' ? 'Make session' : 'Make project'} todo ${row.title}`),
   button('Move up', () => reorder(row,-1),false,`Move up todo ${row.title}`), button('Move down', () => reorder(row,1),false,`Move down todo ${row.title}`)
  ))),
  todoEdit && h('form', {'aria-label': 'Edit todo', onSubmit: e => {e.preventDefault(); let labels; try {labels = JSON.parse(todoEdit.labelsText);if (!Array.isArray(labels)) throw new Error();} catch {setError('Labels must be a JSON array.');return;} void mutate(`/todos/${encode(todoEdit.id)}`, 'PUT', {title: todoEdit.title, description: todoEdit.description, status: todoEdit.status, priority: todoEdit.priority, labels}, () => setTodoEdit(null));}},
   field('Todo title',todoEdit.title,v=>setTodoEdit({...todoEdit,title:v}),{required:true}),field('Description',todoEdit.description,v=>setTodoEdit({...todoEdit,description:v})),
   select('Status',todoEdit.status,v=>setTodoEdit({...todoEdit,status:v}),['pending','in_progress','done','blocked'].map(v=>[v,v])),select('Priority',todoEdit.priority,v=>setTodoEdit({...todoEdit,priority:v}),['low','medium','high','critical'].map(v=>[v,v])),
   field('Labels',todoEdit.labelsText,v=>setTodoEdit({...todoEdit,labelsText:v})),h('button',{disabled:busy},'Save todo'),button('Cancel edit',()=>setTodoEdit(null))),
  h('h3', null, 'Plans'), plans.length === 0 && h('p', null, 'No plans.'),
  ...plans.map(row => h('article', {key: row.id, 'aria-label': row.title}, h('h4', null, row.title), h('small', null, `${row.scope} · ${row.status}`), row.description && h('p', null, row.description),
   button('Edit plan',()=>setPlanEdit({...row}),false,`Edit plan ${row.title}`),button('Delete plan',()=>{if(window.confirm('Delete this plan?'))void mutate(`/plans/${encode(row.id)}`,'DELETE');},false,`Delete plan ${row.title}`),
   row.status === 'proposed' && h(React.Fragment,null,
    button('Approve plan',()=>void mutate(`/plans/${encode(row.id)}/approve`,'POST',{create_todos:false}),false,`Approve plan ${row.title}`),
    button('Approve and create todos',()=>void mutate(`/plans/${encode(row.id)}/approve`,'POST',{create_todos:true}),row.scope==='workspace',`Approve and create todos for plan ${row.title}`),
    button('Reject plan',()=>void mutate(`/plans/${encode(row.id)}`,'PUT',{status:'abandoned'}),false,`Reject plan ${row.title}`)),
   h('ol',null,...(Array.isArray(row.steps) ? row.steps.filter(step => step && typeof step === 'object') : []).map((step,i)=>h('li',{key:step.id || i},
    h('label',null,h('input',{type:'checkbox',checked:step.status==='done',disabled:busy||!step.id,onChange:e=>void mutate(`/plans/${encode(row.id)}/steps/${encode(step.id)}`,'PUT',{status:e.target.checked?'done':'pending'})}),step.title),
    h('small',null,` ${step.status || 'pending'}`), Array.isArray(step.depends_on)&&step.depends_on.length>0&&h('p',null,`Depends on: ${step.depends_on.join(', ')}`),step.acceptance&&h('p',null,step.acceptance),step.notes&&h('p',null,step.notes),
    button('Edit step',()=>{const notes=window.prompt('Step notes',step.notes || '');if(notes!==null)void mutate(`/plans/${encode(row.id)}/steps/${encode(step.id)}`,'PUT',{notes});},!step.id,`Edit step ${step.title || step.id || i} in plan ${row.title}`)
   ))),
   h(StepForm,{disabled:busy||!current,onAdd:step=>void mutate(`/plans/${encode(row.id)}/steps`,'POST',{steps:[step]})})
  )),
  planEdit && h('form',{'aria-label':'Edit plan',onSubmit:e=>{e.preventDefault();void mutate(`/plans/${encode(planEdit.id)}`,'PUT',{title:planEdit.title,description:planEdit.description,status:planEdit.status},()=>setPlanEdit(null));}},
   field('Plan title',planEdit.title,v=>setPlanEdit({...planEdit,title:v}),{required:true}),field('Plan description',planEdit.description,v=>setPlanEdit({...planEdit,description:v})),
   select('Plan status',planEdit.status,v=>setPlanEdit({...planEdit,status:v}),['proposed','approved','in_progress','complete','abandoned'].map(v=>[v,v])),h('button',{disabled:busy},'Save plan'),button('Cancel plan edit',()=>setPlanEdit(null)))
 );
}
function StepForm({disabled,onAdd}) {
 const [title,setTitle]=useState(''),[acceptance,setAcceptance]=useState('');
 return h('form',{onSubmit:e=>{e.preventDefault();if(title.trim()){onAdd({title:title.trim(),acceptance});setTitle('');setAcceptance('');}}},
  h('label',null,'Step title',h('input',{value:title,onChange:e=>setTitle(e.target.value),required:true})),
  h('label',null,'Acceptance',h('input',{value:acceptance,onChange:e=>setAcceptance(e.target.value)})),h('button',{disabled},'Add step'));
}
