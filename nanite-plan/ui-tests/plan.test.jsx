import React from 'react';
import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest';
import {cleanup, fireEvent, render, screen, waitFor, within} from '@testing-library/react';
import {PlanPanel} from '../ui/index.js';

let todos, plans, requests;
const response = value => ({ok:true,json:async()=>value,text:async()=>''});
beforeEach(()=>{
 todos=[{id:'t1',title:'First todo',scope:'session',scope_id:'a',status:'pending',priority:'medium',description:'Details',metadata:{},labels:[],created_at:'2026-01-01'}];
 plans=[{id:'p1',title:'Existing plan',scope:'session',scope_id:'a',status:'proposed',description:'',steps:[{id:'s1',title:'First step',status:'pending',depends_on:[],acceptance:'Must pass',notes:''}]}];requests=[];
 vi.stubGlobal('fetch',vi.fn(async(path,options={})=>{
  const url=new URL(path,'http://localhost');const resource=url.pathname.split('/')[4];const id=url.pathname.split('/')[5];const method=options.method ?? 'GET';const body=options.body ? JSON.parse(options.body) : undefined;requests.push({url,method,body});
  if(method==='GET'){
   const scope=url.searchParams.get('scope');const scopeId=url.searchParams.get('scope_id');let rows=(resource==='todos'?todos:plans).filter(row=>(!scope||row.scope===scope)&&(!scopeId||row.scope_id===scopeId));
   const offset=Number(url.searchParams.get('offset') ?? 0);return response({[resource]:rows.slice(offset,offset+100),more:offset+100<rows.length,next_offset:Math.min(offset+100,rows.length),project_id:'project-a'});
  }
  if(method==='POST'&&(resource==='todos'||resource==='plans')&&!id){(resource==='todos'?todos:plans).push({id:'new',...body,status:resource==='todos'?'pending':'proposed',priority:'medium',metadata:{},labels:[],steps:[],created_at:'2026-02-01'});}
  if(method==='PUT'&&resource==='todos'){Object.assign(todos.find(row=>row.id===id),body);}
  if(method==='DELETE'&&resource==='todos'){todos=todos.filter(row=>row.id!==id);}
  return response({ok:true});
 }));
 vi.stubGlobal('confirm',vi.fn(() => true));
});
afterEach(()=>{cleanup();vi.restoreAllMocks();vi.unstubAllGlobals();});
const mount=async()=>{const view=render(<PlanPanel session_id="a"/>);await screen.findByText('First todo');return view;};

describe('Plan panel',()=>{
 it('loads session/project work, trims creates and renders text safely',async()=>{
  todos[0].description='<img src=x onerror=alert(1)>';
  const view=await mount();expect(view.container.querySelector('img')).toBeNull();expect(screen.getByText('<img src=x onerror=alert(1)>')).toBeTruthy();
  fireEvent.change(screen.getByLabelText('Title'),{target:{value:'  New work  '}});fireEvent.click(screen.getByText('Add work'));
  await waitFor(()=>expect(requests.some(r=>r.method==='POST'&&r.body.title==='New work'&&r.body.scope_id==='a')).toBe(true));
  await screen.findByText('New work');expect(view.container.querySelector('section').style.overflowY).toBe('auto');
 });
 it('toggles, edits and deletes a todo through owned routes',async()=>{
  await mount();fireEvent.click(screen.getByLabelText('First todo'));
  await waitFor(()=>expect(requests.some(r=>r.method==='PUT'&&r.body.status==='done')).toBe(true));await screen.findByText('First todo');
  fireEvent.click(screen.getByText('Edit todo'));fireEvent.change(screen.getByLabelText('Todo title'),{target:{value:'Renamed'}});fireEvent.change(screen.getByLabelText('Priority'),{target:{value:'critical'}});fireEvent.click(screen.getByText('Save todo'));
  await screen.findByText('Renamed');expect(requests.some(r=>r.method==='PUT'&&r.body.title==='Renamed'&&r.body.priority==='critical')).toBe(true);
  fireEvent.click(screen.getByText('Delete todo'));await waitFor(()=>expect(screen.queryByText('Renamed')).toBeNull());expect(requests.some(r=>r.method==='DELETE')).toBe(true);
 });
 it('promotes todos and exposes project and workspace filters',async()=>{
  await mount();fireEvent.click(screen.getByText('Make project'));await waitFor(()=>expect(requests.some(r=>r.method==='PATCH'&&r.body.scope==='project'&&r.body.project_id==='project-a')).toBe(true));
  fireEvent.change(screen.getByLabelText('View'),{target:{value:'project'}});await waitFor(()=>expect(screen.queryByText('First todo')).toBeNull());
  fireEvent.change(screen.getByLabelText('Add'),{target:{value:'plan'}});fireEvent.change(screen.getByLabelText('Scope'),{target:{value:'workspace'}});fireEvent.change(screen.getByLabelText('Title'),{target:{value:'Workspace plan'}});fireEvent.click(screen.getByText('Add work'));
  await waitFor(()=>expect(requests.some(r=>r.method==='POST'&&r.body.scope==='workspace')).toBe(true));
 });
 it('appends steps, toggles and approves without replacing existing steps',async()=>{
  await mount();const article=screen.getByRole('article',{name:'Existing plan'});fireEvent.change(within(article).getByLabelText('Step title'),{target:{value:'Next step'}});fireEvent.change(within(article).getByLabelText('Acceptance'),{target:{value:'Reviewed'}});fireEvent.click(within(article).getByText('Add step'));
  await waitFor(()=>expect(requests.some(r=>r.method==='POST'&&r.url.pathname.endsWith('/p1/steps')&&r.body.steps[0].acceptance==='Reviewed')).toBe(true));
  await screen.findByText('Existing plan');fireEvent.click(screen.getByLabelText('First step'));await waitFor(()=>expect(requests.some(r=>r.method==='PUT'&&r.url.pathname.endsWith('/steps/s1')&&r.body.status==='done')).toBe(true));
  await screen.findByText('Existing plan');fireEvent.click(screen.getByText('Approve and create todos'));await waitFor(()=>expect(requests.some(r=>r.url.pathname.endsWith('/approve')&&r.body.create_todos)).toBe(true));
 });
 it('refreshes on focus and visibility and cancels timers on unmount',async()=>{
  const interval=vi.spyOn(globalThis,'setInterval'),clear=vi.spyOn(globalThis,'clearInterval');const view=await mount();expect(interval.mock.calls.some(call=>call[1]===30000)).toBe(true);
  const before=requests.length;fireEvent(window,new Event('focus'));await waitFor(()=>expect(requests.length).toBeGreaterThan(before));await screen.findByText('First todo');
  const second=requests.length;fireEvent(document,new Event('visibilitychange'));await waitFor(()=>expect(requests.length).toBeGreaterThan(second));view.unmount();expect(clear).toHaveBeenCalled();
 });
 it('paginates whole records and prevents old-session state appearing after a switch',async()=>{
  todos=Array.from({length:101},(_,i)=>({...todos[0],id:'t'+i,title:'Todo '+i}));const view=render(<PlanPanel session_id="a"/>);await screen.findByText('Todo 100');expect(requests.some(r=>r.url.searchParams.get('offset')==='100')).toBe(true);
  view.rerender(<PlanPanel session_id="b"/>);expect(screen.queryByText('Todo 0')).toBeNull();await screen.findByText('No todos.');
  expect(requests.filter(r=>r.url.searchParams.get('session_id')==='b').length).toBeGreaterThan(0);
 });
});

describe('review regressions',()=>{
 it('retains drafts and an open edit form across an unrelated mutation, with oldest rows first',async()=>{
  todos.push({...todos[0],id:'t2',title:'Second todo',created_at:'2026-02-01'});
  await mount();const rows=screen.getAllByRole('listitem');expect(rows[0].textContent).toContain('First todo');
  fireEvent.change(screen.getByLabelText('Title'),{target:{value:'Draft work'}});
  fireEvent.click(screen.getByRole('button',{name:'Edit todo First todo (t1)'}));fireEvent.change(screen.getByLabelText('Todo title'),{target:{value:'Unsubmitted edit'}});
  fireEvent.click(screen.getByLabelText('Second todo'));await waitFor(()=>expect(requests.some(r=>r.method==='PUT'&&r.url.pathname.endsWith('/t2'))).toBe(true));
  await waitFor(()=>expect(screen.getByRole('button',{name:'Edit todo First todo (t1)'}).disabled).toBe(false));
  expect(screen.getByLabelText('Title').value).toBe('Draft work');expect(screen.getByLabelText('Todo title').value).toBe('Unsubmitted edit');
 });
 it('renders malformed step shapes without crashing',async()=>{
  plans[0].steps=[null,{id:'s2',title:'Malformed dependencies',depends_on:'s1'},{}];await mount();expect(screen.getByText('Malformed dependencies')).toBeTruthy();expect(screen.queryByText('Depends on: s1')).toBeNull();
 });
 it('displays load and mutation errors',async()=>{
  const normal=fetch;vi.stubGlobal('fetch',vi.fn(async(path,options)=>options?.method ? {ok:false,text:async()=> 'Mutation refused'} : normal(path,options)));
  await mount();fireEvent.click(screen.getByLabelText('First todo'));await screen.findByRole('alert');expect(screen.getByRole('alert').textContent).toBe('Mutation refused');
  cleanup();vi.stubGlobal('fetch',vi.fn(async()=>({ok:false,text:async()=> 'Read failed'})));render(<PlanPanel session_id="a"/>);await screen.findByRole('alert');expect(screen.getByRole('alert').textContent).toBe('Read failed');
 });
 it('ignores an in-flight mutation completion after switching sessions',async()=>{
  const normal=fetch;let finish;vi.stubGlobal('fetch',vi.fn(async(path,options)=>options?.method ? new Promise(resolve=>{finish=resolve;}) : normal(path,options)));
  const view=await mount();fireEvent.click(screen.getByLabelText('First todo'));await waitFor(()=>expect(finish).toBeTypeOf('function'));
  view.rerender(<PlanPanel session_id="b"/>);await screen.findByText('No todos.');fireEvent.change(screen.getByLabelText('Title'),{target:{value:'B draft'}});
  finish(response({ok:true}));await waitFor(()=>expect(screen.getByLabelText('Title').value).toBe('B draft'));expect(screen.queryByText('First todo')).toBeNull();
 });
});

describe('round-two regressions',()=>{
 it('coerces malformed step content and uniquely labels identically named rows',async()=>{
  todos.push({...todos[0],id:'t2'});plans[0].steps=[{id:'s1',title:{bad:'title'},notes:{bad:'notes'},acceptance:{bad:'acceptance'}},{id:'s2',title:'Duplicate'},{id:'s3',title:'Duplicate'}];
  const view=render(<PlanPanel session_id="a"/>);await screen.findByRole('button',{name:'Edit todo First todo (t1)'});expect(screen.getByRole('button',{name:'Edit todo First todo (t2)'})).toBeTruthy();
  expect(view.container.textContent).toContain('[object Object]');expect(screen.getByRole('button',{name:'Edit step Duplicate (s2) in plan Existing plan (p1)'})).toBeTruthy();expect(screen.getByRole('button',{name:'Edit step Duplicate (s3) in plan Existing plan (p1)'})).toBeTruthy();
 });
});
