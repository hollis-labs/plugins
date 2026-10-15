import React, {useEffect, useState, useRef} from 'react';
const h=React.createElement;
const base='/api/plugins/nanite.reminders';
export function RemindersTab({session_id:sessionId}) {
 const lifetime=useRef({session:sessionId,epoch:0});
 if(lifetime.current.session!==sessionId)lifetime.current={session:sessionId,epoch:lifetime.current.epoch+1};
 const wanted=useRef(100);const paging=useRef(false);
 const [rows,setRows]=useState([]),[projectId,setProjectId]=useState(''),[more,setMore]=useState(false),[error,setError]=useState(''),[busy,setBusy]=useState(false),[revision,setRevision]=useState(0),[text,setText]=useState(''),[at,setAt]=useState(''),[scope,setScope]=useState('session'),[offset,setOffset]=useState(0);
 // Include session identity in visible state so a switched session cannot
 // display or mutate the previous session's reminders before effects run.
 const [loadedSession,setLoadedSession]=useState(null);
 useEffect(()=>{
  const abort=new AbortController();let current=true;let loading=false;
  wanted.current=100;paging.current=false;setBusy(false);setRows([]);setOffset(0);setError('');setLoadedSession(null);
  const load=async()=>{
   if(loading||paging.current)return;loading=true;
   try{let all=[];let data;let cursor=0;
    do{const suffix=cursor?`&offset=${cursor}`:'';const response=await fetch(`${base}/reminders?session_id=${encodeURIComponent(sessionId)}${suffix}`,{signal:abort.signal});if(!response.ok)throw new Error('Could not load reminders');data=await response.json();all.push(...data.reminders);if(data.more&&data.next_offset<=cursor)throw new Error('Invalid reminder pagination');cursor=data.next_offset;}while(current&&data.more&&cursor<wanted.current);
    if(current){setRows(all);setMore(data.more);setOffset(data.next_offset);setProjectId(data.project_id);setLoadedSession(sessionId);setError('');}}
   catch(e){if(current&&e.name!=='AbortError')setError(e.message);}finally{loading=false;}
  };
  if(sessionId)void load();const interval=sessionId?setInterval(load,5000):null;
  return()=>{current=false;abort.abort();if(interval)clearInterval(interval);};
 },[sessionId,revision]);
 const mutate=async(path,method,body)=>{
  if(!sessionId||loadedSession!==sessionId||busy)return;
  const operationSession=sessionId;const epoch=lifetime.current.epoch;const live=()=>lifetime.current.epoch===epoch;setBusy(true);setError('');
  try{const response=await fetch(`${base}${path}?session_id=${encodeURIComponent(operationSession)}`,{method,headers:body?{'Content-Type':'application/json'}:undefined,body:body?JSON.stringify(body):undefined});if(!response.ok)throw new Error('Reminder change failed');if(live())setRevision(r=>r+1);}
  catch(e){if(live())setError(e.message);}finally{if(live())setBusy(false);}
 };
 const next=async()=>{
  if(busy||paging.current||loadedSession!==sessionId)return;paging.current=true;const epoch=lifetime.current.epoch;const live=()=>lifetime.current.epoch===epoch;setBusy(true);
  try{const response=await fetch(`${base}/reminders?session_id=${encodeURIComponent(sessionId)}&offset=${offset}`);if(!response.ok)throw new Error('Could not load more reminders');const data=await response.json();if(live()){wanted.current=data.next_offset;setRows(r=>[...r,...data.reminders]);setMore(data.more);setOffset(data.next_offset);}}
  catch(e){if(live())setError(e.message);}finally{if(live()){paging.current=false;setBusy(false);}}
 };
 if(!sessionId)return h('p',null,'Select a session to view reminders.');
 const shown=loadedSession===sessionId?rows:[];
 return h('section',{'aria-label':'Reminders'},
  h('p',null,'Due reminders stay pending until acknowledged.'),
  error&&h('p',{role:'alert'},error),
  h('form',{onSubmit:e=>{e.preventDefault();const date=new Date(at);if(!Number.isFinite(date.getTime())){setError('Choose a valid reminder time');return;}void mutate('/reminders','POST',{text,trigger:{type:'time',at:date.toISOString()},scope});}},
   h('label',null,'Reminder',h('input',{value:text,onChange:e=>setText(e.target.value),maxLength:8192,required:true})),
   h('label',null,'Time',h('input',{type:'datetime-local',value:at,onChange:e=>setAt(e.target.value),required:true})),
   h('label',null,'Scope',h('select',{value:scope,onChange:e=>setScope(e.target.value)},h('option',{value:'session'},'Session'),h('option',{value:'project',disabled:!projectId},'Project'))),
   h('button',{type:'submit',disabled:busy||loadedSession!==sessionId},'Set reminder')),
  shown.length===0&&h('p',null,'No pending reminders.'),
  h('ul',null,...shown.map(row=>h('li',{key:row.id},
   h('p',null,row.text),h('small',null,`${row.scope} · ${row.trigger_json}`),
   h('button',{disabled:busy,onClick:()=>void mutate(`/reminders/${encodeURIComponent(row.id)}/ack`,'POST')},'Acknowledge'),
   h('button',{disabled:busy,onClick:()=>void mutate(`/reminders/${encodeURIComponent(row.id)}`,'DELETE')},'Delete'),
   h('button',{disabled:busy||(row.scope!=='project'&&!projectId),onClick:()=>void mutate(`/reminders/${encodeURIComponent(row.id)}/scope`,'PATCH',{scope:row.scope==='project'?'session':'project'})},row.scope==='project'?'Make session':'Make project')
  ))),more&&h('button',{disabled:busy,onClick:()=>void next()},'Load more')
 );
}
