<!--
  Dashboardws — root of the WORKSPACES ("ws") dashboard.

  LAYOUT PHILOSOPHY · OS VIRTUAL DESKTOPS
  ───────────────────────────────────────
  A WORKSPACE is a named, saved, fully-composed desktop — its own recursive
  split-tree of panes. The operator creates / names / reorders / deletes /
  duplicates workspaces and switches between them like Windows / Ubuntu
  virtual desktops. Each workspace can hold ANY mix of the 7 pane types
  (terminal / kanban / events / transcript / inspector / mesh / tasks), so
  workspaces SUBSUME the dock / grid / chat ideas as user-made arrangements.

  · STRIP — the header strip (WsStrip) shows the workspaces; '+' adds one,
    double-click / right-click renames, drag reorders. Numeric badges hint
    the Ctrl+N shortcut.
  · SWITCH — Ctrl+1..9 jump to workspace N; Ctrl+` and Ctrl+Tab cycle
    next/prev (reused muscle memory); clicking the strip also switches.
  · PERSIST — the full workspace list + active id + per-workspace focus/max
    are saved per-user to localStorage (survives reloads), seeded with 3
    editable/deletable defaults (Terminals / Board / Conversation) on first
    run.

  How each HARD REQUIREMENT is met:
  #1 Arrow-collapse EVERY region — roster + inspector wrap in <WsPanel>
     (chevron on the edge, no hamburger); each PANE keeps its own chevron.
  #2 Full-screen EVERY pane — roster + inspector carry ⛶; each pane has its
     own maximize/restore.
  #3 FULLY RESPONSIVE — below the breakpoint side panels become slide-in
     overlays, the pane grid reflows, headers never overflow (tight →
     overflow menu). Holds at ~700 / ~1100 / ~1600.
  #4 ZERO capability gaps — Kanban / Events / Tasks / Mesh are PANE TYPES,
     not Settings. The gear modal (WsSettings) holds config only + the three
     formerly-missing capabilities (Runtime/Global, Grants, Review-axes).
  #5 Real data + live xterm — calm data layer (sessions/teams/memberships/
     events @2.5s + EventSource) + WidgetTerminal; agentIdentity colors/icons
     everywhere; both themes; user / sign-out menu.
  #6 ESC closes popups/menus/modals/overlays in priority order; right-click
     context menus on the workspace strip + on panes.

  Self-contained under components/ws/. Reuses calm/* + v08 widgets read-only.
  Owns its own --calm-* tokens (two full palettes) so it never collides.
-->
<script>
  import { onMount } from 'svelte';
  import '@xterm/xterm/css/xterm.css';
  import WsPane from './WsPane.svelte';
  import WsPanel from './WsPanel.svelte';
  import WsStrip from './WsStrip.svelte';
  import WsSettings from './WsSettings.svelte';
  import CalmRail from '../calm/CalmRail.svelte';
  import CalmInspector from '../calm/CalmInspector.svelte';
  import SpawnWizardV9 from '../v09/SpawnWizardV9.svelte';
  import { registerRoster } from '../../lib/agentIdentity.js';
  import * as T from './layoutWs.js';

  let { version = 'ws' } = $props();

  // ---- fetch-auth wrapper (idempotent; shared with every chepherd route) ----
  if (typeof window !== 'undefined' && !window.__chepherdFetchPatched) {
    window.__chepherdFetchPatched = true;
    const _origFetch = window.fetch.bind(window);
    window.fetch = (input, init) => {
      const url = typeof input === 'string' ? input : (input?.url || '');
      if (url.startsWith('/api/') || url.startsWith('/api-v')) {
        let tok = '';
        try { tok = localStorage.getItem('chepherd-token') || ''; } catch {}
        init = init || {};
        init.headers = new Headers(init.headers || (typeof input !== 'string' ? input.headers : undefined));
        if (tok && !init.headers.has('Authorization')) init.headers.set('Authorization', 'Bearer ' + tok);
        return _origFetch(input, init).then((r) => {
          if (r.status === 401) { try { window.dispatchEvent(new CustomEvent('chepherd-401')); } catch {} }
          return r;
        });
      }
      return _origFetch(input, init);
    };
  }

  const API = '/api/v1';

  // ---- data state ----
  let sessions = $state([]);
  let teams = $state([]);
  let memberships = $state([]);
  let events = $state([]);
  let peers = $state([]);
  let tasks = $state([]);
  let selectedAgent = $state('');
  let mruFocus = $state([]);

  // ---- workspaces (the desktops) ----
  // Each: { id, name, layout }. Seeded on first run, persisted to localStorage.
  let workspaces = $state([]);
  let activeId = $state('');
  let wsFocus = $state({});   // workspaceId -> focusedLeafId
  let wsMax = $state({});     // workspaceId -> maximizedLeafId
  let persistReady = $state(false);

  let activeWs = $derived(workspaces.find((w) => w.id === activeId) || null);
  let focusedLeafId = $derived(activeWs ? (wsFocus[activeWs.id] || '') : '');
  let maximizedLeafId = $derived(activeWs ? (wsMax[activeWs.id] || '') : '');

  // ---- persistent regions ----
  let railOpen = $state(true);
  let contextOpen = $state(true);
  let maximizedRegion = $state('');    // '' | roster | inspector | canvas

  // ---- chrome / overlays ----
  let theme = $state('dark');
  let fontSize = $state(14);
  let showSettings = $state(false);
  let showWizard = $state(false);
  let needLogin = $state(false);
  let notice = $state('');
  let userMenuOpen = $state(false);
  let overflowOpen = $state(false);
  let mounted = $state(false);

  // ---- responsive ----
  let narrow = $state(false);
  let tight = $state(false);
  let railOverlay = $state(false);
  let ctxOverlay = $state(false);

  // ---- rename + context menus ----
  let renamingId = $state('');
  let renameVal = $state('');
  let ctxMenu = $state(null);          // { x, y, items:[{label, danger?, onpick}] } | null

  function flash(msg) { notice = msg; setTimeout(() => { if (notice === msg) notice = ''; }, 2600); }

  // ---------------- theme + font ----------------
  function applyTheme(t) {
    theme = t === 'light' ? 'light' : 'dark';
    try { document.documentElement.dataset.theme = theme; } catch {}
    try { localStorage.setItem('chepherd-theme', theme); } catch {}
  }
  function toggleTheme() { applyTheme(theme === 'dark' ? 'light' : 'dark'); }
  function applyFont(delta) {
    fontSize = Math.max(9, Math.min(22, fontSize + delta));
    try {
      document.documentElement.style.setProperty('--ws-font', fontSize + 'px');
      localStorage.setItem('chepherd-font', String(fontSize));
    } catch {}
  }

  // ---------------- data ----------------
  function getToken() { try { return localStorage.getItem('chepherd-token') || ''; } catch { return ''; } }
  function pushMRU(name) { if (!name) return; mruFocus = [name, ...mruFocus.filter((n) => n !== name)].slice(0, 12); }

  async function refresh() {
    try {
      const [s, t, m, ev] = await Promise.all([
        fetch(`${API}/sessions`).then((r) => r.json()),
        fetch(`${API}/teams`).then((r) => r.json()),
        fetch(`${API}/memberships`).then((r) => r.json()),
        fetch(`${API}/events?limit=80`).then((r) => r.json()),
      ]);
      sessions = s.sessions || [];
      registerRoster(
        [...sessions]
          .sort((a, b) => (a.created_at || '').localeCompare(b.created_at || '') || a.name.localeCompare(b.name))
          .map((x) => x.name)
      );
      teams = t.teams || [];
      memberships = m.memberships || [];
      events = ev.events || [];

      if (selectedAgent && !sessions.find((x) => x.name === selectedAgent)) {
        const fb = mruFocus.find((n) => n !== selectedAgent && sessions.find((x) => x.name === n && !x.exited));
        selectedAgent = fb || '';
      }
      if (!selectedAgent && sessions.length) {
        const w = sessions.find((x) => !x.exited && x.role !== 'shepherd') || sessions.find((x) => !x.exited);
        if (w) { selectedAgent = w.name; bindFocusedTerminal(w.name, false); }
      }
    } catch {}
  }

  // Lightweight pollers for mesh + tasks panes (only fetched while present).
  let hasMeshPane = $derived(workspaces.some((w) => T.leaves(w.layout).some((l) => l.widget === 'mesh')));
  let hasTasksPane = $derived(workspaces.some((w) => T.leaves(w.layout).some((l) => l.widget === 'tasks')));
  async function loadMesh() { try { const r = await fetch(`${API}/peers`); if (r.ok) { const j = await r.json(); peers = j.peers || []; } } catch {} }
  async function loadTasks() { try { const r = await fetch(`${API}/tasks`); if (r.ok) { const j = await r.json(); tasks = j.tasks || []; } } catch {} }

  let evStream = null;
  function startEventStream() {
    if (evStream) return;
    const tok = getToken();
    const q = tok ? '?token=' + encodeURIComponent(tok) : '';
    try {
      evStream = new EventSource(`${API}/events/stream${q}`);
      evStream.onmessage = (e) => { try { events = [...events, JSON.parse(e.data)].slice(-200); } catch {} };
      evStream.onerror = () => { evStream?.close(); evStream = null; setTimeout(startEventStream, 3000); };
    } catch {}
  }

  // ---------------- active-workspace layout helpers ----------------
  function curLayout() { return activeWs?.layout || null; }
  function setCurLayout(next) {
    if (!activeWs) return;
    workspaces = workspaces.map((w) => (w.id === activeWs.id ? { ...w, layout: next } : w));
  }
  function setFocusedLeaf(id) { if (activeWs) wsFocus = { ...wsFocus, [activeWs.id]: id }; }
  function setMaxLeaf(id) { if (activeWs) wsMax = { ...wsMax, [activeWs.id]: id }; }

  function firstTerminalLeafId(layout) { const tl = T.leaves(layout).find((l) => l.widget === 'terminal'); return tl ? tl.id : ''; }
  function ensureFocusedLeaf(layout) { const all = T.leaves(layout); if (!all.find((l) => l.id === focusedLeafId)) setFocusedLeaf(all[0]?.id || ''); }

  function selectAgent(name) {
    selectedAgent = name;
    pushMRU(name);
    bindFocusedTerminal(name, true);
    if (narrow) railOverlay = false;
  }

  function bindFocusedTerminal(name, flashIt) {
    if (!activeWs) return;
    let layout = activeWs.layout;
    ensureFocusedLeaf(layout);
    let fid = wsFocus[activeWs.id] || T.leaves(layout)[0]?.id || '';
    let target = T.findLeaf(layout, fid);
    if (!target || target.widget !== 'terminal') {
      const tid = firstTerminalLeafId(layout);
      if (tid) { fid = tid; target = T.findLeaf(layout, tid); }
    }
    if (target && target.widget === 'terminal') {
      layout = T.setLeafConfig(layout, target.id, { agent: name });
      wsFocus = { ...wsFocus, [activeWs.id]: target.id };
      if (flashIt) flash(`Focus → ${name}`);
    } else if (fid) {
      layout = T.setLeafWidget(layout, fid, 'terminal', { agent: name });
      wsFocus = { ...wsFocus, [activeWs.id]: fid };
    }
    workspaces = workspaces.map((w) => (w.id === activeWs.id ? { ...w, layout } : w));
  }

  function openAgentInNewPane(name) {
    if (!activeWs) return;
    let layout = activeWs.layout;
    const base = wsFocus[activeWs.id] || T.leaves(layout)[0]?.id;
    if (!base) {
      layout = T.leaf('terminal', { agent: name });
      wsFocus = { ...wsFocus, [activeWs.id]: T.leaves(layout)[0].id };
    } else {
      const { tree, newId } = T.splitLeaf(layout, base, 'h', 'terminal', { agent: name });
      layout = tree;
      wsFocus = { ...wsFocus, [activeWs.id]: newId };
    }
    workspaces = workspaces.map((w) => (w.id === activeWs.id ? { ...w, layout } : w));
    selectedAgent = name; pushMRU(name);
    flash(`Opened ${name} in a new pane`);
    if (narrow) railOverlay = false;
  }

  // pane operations (active workspace)
  function onFocusLeaf(id) {
    setFocusedLeaf(id);
    const leaf = T.findLeaf(curLayout(), id);
    if (leaf?.widget === 'terminal' && leaf.config?.agent) { selectedAgent = leaf.config.agent; pushMRU(selectedAgent); }
  }
  function onSetRatio(splitId, ratio) { setCurLayout(T.setSplitRatio(curLayout(), splitId, ratio)); }
  function onSplit(leafId, dir) {
    const cur = T.findLeaf(curLayout(), leafId);
    const cfg = cur?.widget === 'terminal' ? { agent: cur.config?.agent || selectedAgent } : {};
    const { tree, newId } = T.splitLeaf(curLayout(), leafId, dir, 'terminal', cfg);
    setCurLayout(tree); setFocusedLeaf(newId);
  }
  function onCloseLeaf(leafId) {
    if (T.countLeaves(curLayout()) <= 1) { flash('At least one pane stays open'); return; }
    if (maximizedLeafId === leafId) setMaxLeaf('');
    const next = T.removeLeaf(curLayout(), leafId);
    setCurLayout(next);
    if (!T.findLeaf(next, focusedLeafId)) setFocusedLeaf(T.leaves(next)[0]?.id || '');
  }
  function onSetAgent(leafId, name) {
    setCurLayout(T.setLeafConfig(curLayout(), leafId, { agent: name }));
    setFocusedLeaf(leafId); selectedAgent = name; pushMRU(name);
  }
  function onSetWidget(leafId, widget) {
    const cfg = widget === 'terminal' ? { agent: selectedAgent } : {};
    setCurLayout(T.setLeafWidget(curLayout(), leafId, widget, cfg));
    setFocusedLeaf(leafId);
  }
  function onMaximizeLeaf(leafId) {
    const wasMax = maximizedLeafId === leafId;
    setMaxLeaf(wasMax ? '' : leafId);
    if (!wasMax) setFocusedLeaf(leafId);
  }
  function onCollapseLeaf(leafId) {
    const layout = curLayout();
    const p = T.parentOf(layout, leafId);
    if (!p) { flash('Single pane — nothing to collapse'); return; }
    const small = T.collapsedSide(p.split);
    const isCollapsed = small === p.side;
    setCurLayout(T.collapseLeaf(layout, leafId, !isCollapsed));
  }
  function addPane() {
    if (!activeWs) return;
    let layout = activeWs.layout;
    const base = wsFocus[activeWs.id] || T.leaves(layout)[0]?.id;
    const agent = selectedAgent || (sessions.find((s) => !s.exited)?.name || '');
    if (!base) { layout = T.leaf('terminal', { agent }); }
    else { const { tree, newId } = T.splitLeaf(layout, base, 'h', 'terminal', { agent }); layout = tree; wsFocus = { ...wsFocus, [activeWs.id]: newId }; }
    workspaces = workspaces.map((w) => (w.id === activeWs.id ? { ...w, layout } : w));
  }

  let focusedSession = $derived(sessions.find((s) => s.name === selectedAgent) || null);

  // ---------------- workspace operations ----------------
  function selectWorkspace(id) { activeId = id; }
  function selectWorkspaceByIndex(i) { if (i >= 0 && i < workspaces.length) activeId = workspaces[i].id; }
  function cycleWorkspace(delta) {
    if (workspaces.length < 2) return;
    const idx = workspaces.findIndex((w) => w.id === activeId);
    const next = (idx + delta + workspaces.length) % workspaces.length;
    activeId = workspaces[next].id;
  }
  function addWorkspace() {
    const w = { id: T.nextId('ws'), name: `Workspace ${workspaces.length + 1}`, layout: T.defaultLayout() };
    workspaces = [...workspaces, w];
    wsFocus = { ...wsFocus, [w.id]: T.leaves(w.layout)[0]?.id || '' };
    activeId = w.id;
    flash(`Added “${w.name}”`);
  }
  function duplicateWorkspace(id) {
    const src = workspaces.find((w) => w.id === id);
    if (!src) return;
    const layout = T.cloneTreeFreshIds(src.layout); // deep clone with FRESH node ids
    const w = { id: T.nextId('ws'), name: `${src.name} copy`, layout };
    const idx = workspaces.findIndex((x) => x.id === id);
    workspaces = [...workspaces.slice(0, idx + 1), w, ...workspaces.slice(idx + 1)];
    wsFocus = { ...wsFocus, [w.id]: T.leaves(w.layout)[0]?.id || '' };
    activeId = w.id;
    flash(`Duplicated “${src.name}”`);
  }
  function deleteWorkspace(id) {
    if (workspaces.length <= 1) { flash('Keep at least one workspace'); return; }
    const idx = workspaces.findIndex((w) => w.id === id);
    workspaces = workspaces.filter((w) => w.id !== id);
    const { [id]: _f, ...rf } = wsFocus; wsFocus = rf;
    const { [id]: _m, ...rm } = wsMax; wsMax = rm;
    if (activeId === id) activeId = (workspaces[idx] || workspaces[idx - 1] || workspaces[0]).id;
  }
  function moveWorkspace(id, delta) {
    const idx = workspaces.findIndex((w) => w.id === id);
    const to = idx + delta;
    if (idx < 0 || to < 0 || to >= workspaces.length) return;
    const arr = [...workspaces];
    const [it] = arr.splice(idx, 1);
    arr.splice(to, 0, it);
    workspaces = arr;
  }
  function reorderWorkspace(from, to) {
    if (from < 0 || to < 0 || from >= workspaces.length || to >= workspaces.length) return;
    const arr = [...workspaces];
    const [it] = arr.splice(from, 1);
    arr.splice(to, 0, it);
    workspaces = arr;
  }
  function startRename(id) {
    const w = workspaces.find((x) => x.id === id);
    if (!w) return;
    renamingId = id; renameVal = w.name;
  }
  function commitRename() {
    if (!renamingId) return;
    const v = renameVal.trim() || (workspaces.find((w) => w.id === renamingId)?.name || 'Workspace');
    workspaces = workspaces.map((w) => (w.id === renamingId ? { ...w, name: v } : w));
    renamingId = ''; renameVal = '';
  }
  function cancelRename() { renamingId = ''; renameVal = ''; }

  // ---------------- region collapse + maximize ----------------
  function toggleRail() {
    if (narrow) { railOverlay = !railOverlay; ctxOverlay = false; return; }
    railOpen = !railOpen; try { localStorage.setItem('ws-rail', railOpen ? '1' : '0'); } catch {}
  }
  function toggleContext() {
    if (narrow) { ctxOverlay = !ctxOverlay; railOverlay = false; return; }
    contextOpen = !contextOpen; try { localStorage.setItem('ws-context', contextOpen ? '1' : '0'); } catch {}
  }
  function maximizeRegion(region) { maximizedRegion = maximizedRegion === region ? '' : region; }

  // ---------------- login ----------------
  let loginInput = $state('');
  let loginError = $state('');
  async function doLogin() {
    const t = loginInput.trim();
    if (!t) { loginError = 'paste the bootstrap token'; return; }
    try { localStorage.setItem('chepherd-token', t); } catch {}
    try { const r = await fetch(`${API}/sessions`); if (r.status === 401) { loginError = 'token rejected'; return; } }
    catch (e) { loginError = String(e); return; }
    needLogin = false; loginError = ''; loginInput = '';
    refresh(); startEventStream();
  }
  function signOut() {
    userMenuOpen = false;
    try { localStorage.removeItem('chepherd-token'); } catch {}
    try { evStream?.close(); } catch {}
    evStream = null;
    needLogin = true;
  }

  // ---------------- context menus (#6) ----------------
  function openWsMenu(w, x, y) {
    closeAllMenus();
    const idx = workspaces.findIndex((x2) => x2.id === w.id);
    ctxMenu = {
      x, y,
      items: [
        { label: 'Rename', onpick: () => startRename(w.id) },
        { label: 'Duplicate', onpick: () => duplicateWorkspace(w.id) },
        { label: 'Move left', disabled: idx <= 0, onpick: () => moveWorkspace(w.id, -1) },
        { label: 'Move right', disabled: idx >= workspaces.length - 1, onpick: () => moveWorkspace(w.id, 1) },
        { label: 'Delete', danger: true, disabled: workspaces.length <= 1, onpick: () => deleteWorkspace(w.id) },
      ],
    };
  }
  function openPaneMenu(leafId, x, y) {
    closeAllMenus();
    setFocusedLeaf(leafId);
    const isMax = maximizedLeafId === leafId;
    const canCollapse = !!T.parentOf(curLayout(), leafId);
    const canClose = T.countLeaves(curLayout()) > 1;
    ctxMenu = {
      x, y,
      items: [
        { label: 'Split right', onpick: () => onSplit(leafId, 'h') },
        { label: 'Split down', onpick: () => onSplit(leafId, 'v') },
        { label: canCollapse ? 'Collapse / expand' : 'Collapse (needs a split)', disabled: !canCollapse, onpick: () => onCollapseLeaf(leafId) },
        { label: isMax ? 'Restore pane' : 'Maximize pane', onpick: () => onMaximizeLeaf(leafId) },
        { label: 'Close pane', danger: true, disabled: !canClose, onpick: () => onCloseLeaf(leafId) },
      ],
    };
  }
  function pickCtx(item) { if (item.disabled) return; ctxMenu = null; item.onpick(); }
  function closeAllMenus() { userMenuOpen = false; overflowOpen = false; ctxMenu = null; }

  // ---------------- keyboard (#switch + #6) ----------------
  function onKey(e) {
    // Workspace switching shortcuts (Ctrl, not when typing in an input).
    const typing = e.target && /^(INPUT|TEXTAREA|SELECT)$/.test(e.target.tagName || '') || e.target?.isContentEditable;
    if ((e.ctrlKey || e.metaKey) && !e.altKey && !typing) {
      if (e.key >= '1' && e.key <= '9') { e.preventDefault(); selectWorkspaceByIndex(Number(e.key) - 1); return; }
      if (e.key === '`') { e.preventDefault(); cycleWorkspace(1); return; }
      if (e.key === 'Tab') { e.preventDefault(); cycleWorkspace(e.shiftKey ? -1 : 1); return; }
    }
    if (e.key !== 'Escape') return;
    // ESC priority order (#6).
    if (ctxMenu) { ctxMenu = null; return; }
    if (showWizard) { showWizard = false; refresh(); return; }
    if (showSettings) { showSettings = false; return; }
    if (userMenuOpen || overflowOpen) { closeAllMenus(); return; }
    if (renamingId) { cancelRename(); return; }
    if (railOverlay || ctxOverlay) { railOverlay = false; ctxOverlay = false; return; }
    if (maximizedRegion) { maximizedRegion = ''; return; }
    if (maximizedLeafId) { setMaxLeaf(''); return; }
  }
  function onGlobalClick() { if (ctxMenu) ctxMenu = null; }

  // ---------------- responsive ----------------
  function applyResponsive() {
    try {
      const w = window.innerWidth;
      narrow = w < 980;
      tight = w < 720;
      if (!narrow) { railOverlay = false; ctxOverlay = false; }
    } catch {}
  }

  // ---------------- persistence ----------------
  // Persist whenever the workspaces array or active id changes (after the
  // initial load completes, so we never clobber stored data on first paint).
  $effect(() => {
    if (!persistReady) return;
    const snap = workspaces.map((w) => ({ id: w.id, name: w.name, layout: w.layout }));
    T.saveWorkspaces(snap);
  });
  $effect(() => {
    if (!persistReady) return;
    T.saveActiveId(activeId);
  });

  // Poll mesh/tasks only while a pane of that type exists.
  $effect(() => {
    if (!persistReady || needLogin) return;
    if (!hasMeshPane && !hasTasksPane) return;
    if (hasMeshPane) loadMesh();
    if (hasTasksPane) loadTasks();
    const iv = setInterval(() => { if (hasMeshPane) loadMesh(); if (hasTasksPane) loadTasks(); }, 4000);
    return () => clearInterval(iv);
  });

  // ---------------- mount ----------------
  onMount(() => {
    mounted = true;
    let t = '';
    try { t = localStorage.getItem('chepherd-theme') || ''; } catch {}
    if (t !== 'light' && t !== 'dark') {
      try { t = window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'; } catch { t = 'dark'; }
    }
    applyTheme(t);

    let f = 14;
    try { f = +(localStorage.getItem('chepherd-font') || 14) || 14; } catch {}
    fontSize = Math.max(9, Math.min(22, f));
    try { document.documentElement.style.setProperty('--ws-font', fontSize + 'px'); } catch {}

    try { railOpen = localStorage.getItem('ws-rail') !== '0'; } catch {}
    try { contextOpen = localStorage.getItem('ws-context') !== '0'; } catch {}

    // ?token= ingest.
    try {
      const urlTok = new URL(location.href).searchParams.get('token');
      if (urlTok) {
        localStorage.setItem('chepherd-token', urlTok);
        const clean = new URL(location.href); clean.searchParams.delete('token');
        history.replaceState(null, '', clean.toString());
      }
    } catch {}

    // Load persisted workspaces, else seed the 3 defaults.
    const loaded = T.loadWorkspaces();
    workspaces = loaded && loaded.length ? loaded : T.seedWorkspaces();
    const savedActive = T.loadActiveId();
    activeId = (savedActive && workspaces.find((w) => w.id === savedActive)) ? savedActive : workspaces[0].id;
    // Seed per-workspace focus to the first leaf of each.
    const focusSeed = {};
    for (const w of workspaces) focusSeed[w.id] = T.leaves(w.layout)[0]?.id || '';
    wsFocus = focusSeed;
    persistReady = true;

    applyResponsive();
    window.addEventListener('resize', applyResponsive);
    window.addEventListener('keydown', onKey);
    window.addEventListener('click', onGlobalClick);

    if (!getToken()) { needLogin = true; return () => cleanup(); }

    refresh();
    startEventStream();
    const iv = setInterval(refresh, 2500);
    const on401 = () => { needLogin = true; };
    window.addEventListener('chepherd-401', on401);

    function cleanup() {
      clearInterval(iv);
      window.removeEventListener('chepherd-401', on401);
      window.removeEventListener('resize', applyResponsive);
      window.removeEventListener('keydown', onKey);
      window.removeEventListener('click', onGlobalClick);
      evStream?.close();
    }
    return cleanup;
  });

  function onWizardClose() { showWizard = false; refresh(); }
</script>

<div class="ws-root" data-calm-theme={theme} class:narrow class:tight>
  {#if !mounted}
    <div class="boot"><span class="boot-mark">workspaces</span></div>
  {:else if needLogin}
    <div class="login">
      <div class="login-card">
        <div class="login-mark">workspaces</div>
        <h1>Welcome back</h1>
        <p>Paste the bootstrap token chepherd printed at startup.</p>
        <textarea bind:value={loginInput} rows="3" placeholder="eyJhbGc…" spellcheck="false" autocomplete="off"></textarea>
        {#if loginError}<div class="login-err">{loginError}</div>{/if}
        <button class="login-btn" onclick={doLogin}>Enter</button>
      </div>
    </div>
  {:else}
    <!-- ===================== TOP BAR ===================== -->
    <header class="topbar">
      <div class="top-left">
        <span class="brand"><span class="brand-dot"></span>workspaces<span class="brand-ver">{version}</span></span>
        <button class="reg-toggle" class:on={narrow ? railOverlay : railOpen} onclick={(e) => { e.stopPropagation(); toggleRail(); }} title="Roster" aria-label="Toggle roster">{(narrow ? railOverlay : railOpen) ? '◀' : '▶'} Roster</button>
      </div>

      <div class="top-center">
        <WsStrip
          {workspaces}
          {activeId}
          {renamingId}
          bind:renameVal
          onselect={selectWorkspace}
          onadd={addWorkspace}
          onstartrename={startRename}
          oncommitrename={commitRename}
          oncancelrename={cancelRename}
          onreorder={reorderWorkspace}
          onctxmenu={openWsMenu}
        />
      </div>

      <div class="top-right">
        {#if tight}
          <div class="overflow-wrap">
            <button class="icon-btn" onclick={(e) => { e.stopPropagation(); overflowOpen = !overflowOpen; }} title="More" aria-label="More controls">⋯</button>
            {#if overflowOpen}
              <div class="overflow-menu" role="menu">
                <button role="menuitem" onclick={(e) => { e.stopPropagation(); overflowOpen = false; addPane(); }}>＋ Pane</button>
                <button role="menuitem" onclick={(e) => { e.stopPropagation(); overflowOpen = false; showWizard = true; }}>✦ Spawn</button>
                <button role="menuitem" onclick={(e) => { e.stopPropagation(); applyFont(-1); }}>A− Smaller</button>
                <button role="menuitem" onclick={(e) => { e.stopPropagation(); applyFont(1); }}>A+ Larger</button>
                <button role="menuitem" onclick={(e) => { e.stopPropagation(); toggleTheme(); }}>{theme === 'dark' ? '☀ Light' : '☾ Dark'}</button>
                <button role="menuitem" onclick={(e) => { e.stopPropagation(); overflowOpen = false; toggleContext(); }}>⫶ Inspector</button>
                <button role="menuitem" onclick={(e) => { e.stopPropagation(); overflowOpen = false; showSettings = true; }}>⚙ Settings</button>
                <button role="menuitem" class="danger" onclick={(e) => { e.stopPropagation(); signOut(); }}>⎋ Sign out</button>
              </div>
            {/if}
          </div>
        {:else}
          <button class="pill-btn" onclick={addPane} title="Add a terminal pane to this workspace">＋ Pane</button>
          <button class="pill-btn accent" onclick={() => (showWizard = true)} title="Spawn agents">✦ Spawn</button>
          <div class="divider-y"></div>
          <button class="icon-btn" onclick={() => applyFont(-1)} title="Smaller text" aria-label="Smaller text">A−</button>
          <button class="icon-btn" onclick={() => applyFont(1)} title="Larger text" aria-label="Larger text">A+</button>
          <button class="icon-btn" onclick={toggleTheme} title="Toggle light / dark" aria-label="Toggle theme">{theme === 'dark' ? '☀' : '☾'}</button>
          <button class="icon-btn" class:on={narrow ? ctxOverlay : contextOpen} onclick={(e) => { e.stopPropagation(); toggleContext(); }} title="Toggle inspector" aria-label="Toggle inspector">⫶</button>
          <button class="icon-btn" onclick={() => (showSettings = true)} title="Settings" aria-label="Settings">⚙</button>
          <div class="divider-y"></div>
          <div class="user-wrap">
            <button class="icon-btn" onclick={(e) => { e.stopPropagation(); userMenuOpen = !userMenuOpen; }} title="Account" aria-label="Account menu" aria-haspopup="menu" aria-expanded={userMenuOpen}>👤</button>
            {#if userMenuOpen}
              <div class="user-menu" role="menu">
                <div class="um-head">Signed in</div>
                <button class="um-item" role="menuitem" onclick={(e) => { e.stopPropagation(); userMenuOpen = false; showSettings = true; }}>⚙ Settings</button>
                <button class="um-item danger" role="menuitem" onclick={(e) => { e.stopPropagation(); signOut(); }}>⎋ Sign out</button>
              </div>
            {/if}
          </div>
        {/if}
      </div>
    </header>

    <!-- ===================== BODY ===================== -->
    <div class="body" class:max-active={!!maximizedRegion}>
      <!-- LEFT: roster -->
      {#if !narrow}
        <aside class="region rail-region" class:collapsed={!railOpen} class:hidden-by-max={maximizedRegion && maximizedRegion !== 'roster'} class:is-max={maximizedRegion === 'roster'}>
          <WsPanel title="Roster" glyph="☰" side="left" collapsed={!railOpen} maximized={maximizedRegion === 'roster'} badge={`${sessions.filter((s) => !s.exited).length}`} oncollapse={toggleRail} onmaximize={() => maximizeRegion('roster')}>
            <div class="rail-host"><CalmRail {sessions} {teams} {memberships} {selectedAgent} onselect={selectAgent} onopennew={openAgentInNewPane} /></div>
          </WsPanel>
        </aside>
      {/if}

      <!-- CENTER: the active workspace desktop -->
      <main class="center" class:hidden-by-max={maximizedRegion && maximizedRegion !== 'canvas'} class:is-max={maximizedRegion === 'canvas'}>
        <section class="canvas">
          <header class="canvas-head">
            <span class="canvas-title"><span class="canvas-glyph">▦</span>{activeWs?.name || 'Workspace'}</span>
            <div class="canvas-ctl">
              <button class="ctl" title="Add a terminal pane" aria-label="Add pane" onclick={addPane}>＋ Pane</button>
              <button class="ctl" title={maximizedRegion === 'canvas' ? 'Restore' : 'Maximize desktop'} aria-label="Maximize desktop" onclick={() => maximizeRegion('canvas')}>{maximizedRegion === 'canvas' ? '🗗' : '⛶'}</button>
            </div>
          </header>
          <div class="canvas-body">
            {#if activeWs}
              {#key activeWs.id}
                <div class="stage-grid">
                  <WsPane
                    node={maximizedLeafId && T.findLeaf(activeWs.layout, maximizedLeafId) ? T.findLeaf(activeWs.layout, maximizedLeafId) : activeWs.layout}
                    {sessions} {events} {peers} {tasks} {focusedSession}
                    {focusedLeafId} {maximizedLeafId}
                    onfocusleaf={onFocusLeaf}
                    onsetratio={onSetRatio}
                    onsplit={onSplit}
                    onclose={onCloseLeaf}
                    onsetagent={onSetAgent}
                    onsetwidget={onSetWidget}
                    onmaximize={onMaximizeLeaf}
                    oncollapse={onCollapseLeaf}
                    onleafctxmenu={openPaneMenu}
                    canClose={T.countLeaves(activeWs.layout) > 1}
                  />
                </div>
              {/key}
            {/if}
          </div>
        </section>
      </main>

      <!-- RIGHT: inspector -->
      {#if !narrow}
        <aside class="region ctx-region" class:collapsed={!contextOpen} class:hidden-by-max={maximizedRegion && maximizedRegion !== 'inspector'} class:is-max={maximizedRegion === 'inspector'}>
          <WsPanel title="Inspector" glyph="◉" side="right" collapsed={!contextOpen} maximized={maximizedRegion === 'inspector'} oncollapse={toggleContext} onmaximize={() => maximizeRegion('inspector')}>
            <div class="ctx-host"><CalmInspector boundSession={focusedSession} {sessions} /></div>
          </WsPanel>
        </aside>
      {/if}
    </div>

    <!-- ===================== RESPONSIVE OVERLAYS ===================== -->
    {#if narrow && railOverlay}
      <button class="scrim" aria-label="Close roster" onclick={() => (railOverlay = false)}></button>
      <aside class="overlay-panel left">
        <WsPanel title="Roster" glyph="☰" side="left" collapsed={false} maximized={false} badge={`${sessions.filter((s) => !s.exited).length}`} oncollapse={() => (railOverlay = false)} onmaximize={() => {}}>
          <div class="rail-host"><CalmRail {sessions} {teams} {memberships} {selectedAgent} onselect={selectAgent} onopennew={openAgentInNewPane} /></div>
        </WsPanel>
      </aside>
    {/if}
    {#if narrow && ctxOverlay}
      <button class="scrim" aria-label="Close inspector" onclick={() => (ctxOverlay = false)}></button>
      <aside class="overlay-panel right">
        <WsPanel title="Inspector" glyph="◉" side="right" collapsed={false} maximized={false} oncollapse={() => (ctxOverlay = false)} onmaximize={() => {}}>
          <div class="ctx-host"><CalmInspector boundSession={focusedSession} {sessions} /></div>
        </WsPanel>
      </aside>
    {/if}

    <!-- ===================== CONTEXT MENU (#6) ===================== -->
    {#if ctxMenu}
      <div class="ctx-menu" role="menu" style={`left:${ctxMenu.x}px; top:${ctxMenu.y}px`}>
        {#each ctxMenu.items as it}
          <button class="ctx-item {it.danger ? 'danger' : ''}" role="menuitem" disabled={it.disabled} onclick={(e) => { e.stopPropagation(); pickCtx(it); }}>{it.label}</button>
        {/each}
      </div>
    {/if}

    {#if notice}<div class="toast" role="status">{notice}</div>{/if}

    {#if showSettings}
      <WsSettings {theme} {fontSize} {events} {sessions} {teams} {focusedSession} ontheme={applyTheme} onfont={applyFont} onclose={() => (showSettings = false)} />
    {/if}

    {#if showWizard}
      <div class="wizard-overlay" role="dialog" aria-label="Spawn agents"><SpawnWizardV9 onclose={onWizardClose} /></div>
    {/if}
  {/if}
</div>

<style>
  /* ===================== THEME TOKENS (two full palettes) ===================== */
  .ws-root[data-calm-theme="dark"] {
    --calm-bg: #0c0e12; --calm-surface: #14171d; --calm-surface-2: #181c23;
    --calm-chip: #1d222b; --calm-chip-hover: #242a34; --calm-input: #0f1217;
    --calm-border: #242a33; --calm-border-strong: #333b47;
    --calm-fg: #eef1f5; --calm-fg-muted: #a7b0bd; --calm-fg-faint: #6b7480;
    --calm-accent: #6ea8fe; --calm-accent-2: #5ce0c6; --calm-ok: #5cd6a0;
    --calm-warn: #f0c060; --calm-danger: #ff7a7a;
    --calm-shadow-sm: 0 1px 2px rgba(0,0,0,0.35);
    --calm-shadow-focus: 0 0 0 1px color-mix(in srgb, var(--calm-accent) 30%, transparent), 0 8px 28px rgba(0,0,0,0.45);
    --calm-shadow-lg: 0 20px 60px rgba(0,0,0,0.55);
    color-scheme: dark;
  }
  .ws-root[data-calm-theme="light"] {
    --calm-bg: #eef1f6; --calm-surface: #ffffff; --calm-surface-2: #f5f7fb;
    --calm-chip: #eef1f6; --calm-chip-hover: #e3e8f0; --calm-input: #ffffff;
    --calm-border: #e2e7ef; --calm-border-strong: #cdd5e1;
    --calm-fg: #1b2330; --calm-fg-muted: #51607a; --calm-fg-faint: #8a97aa;
    --calm-accent: #3b7bff; --calm-accent-2: #14a08a; --calm-ok: #1f9d6b;
    --calm-warn: #b67d18; --calm-danger: #d23d3d;
    --calm-shadow-sm: 0 1px 2px rgba(20,30,50,0.06);
    --calm-shadow-focus: 0 0 0 1px color-mix(in srgb, var(--calm-accent) 28%, transparent), 0 10px 30px rgba(30,50,90,0.12);
    --calm-shadow-lg: 0 24px 64px rgba(30,50,90,0.18);
    color-scheme: light;
  }

  /* bridge tokens for reused v08 widgets */
  .ws-root {
    --bg: var(--calm-bg); --bg-elev: var(--calm-surface); --bg-elevated: var(--calm-surface); --bg-input: var(--calm-input);
    --fg: var(--calm-fg); --fg-muted: var(--calm-fg-muted); --fg-faint: var(--calm-fg-faint); --muted: var(--calm-fg-muted);
    --border: var(--calm-border); --border-strong: var(--calm-border-strong);
    --accent: var(--calm-accent); --accent-2: var(--calm-accent-2); --danger: var(--calm-danger); --success: var(--calm-ok);
    --select-bg: color-mix(in srgb, var(--calm-accent) 16%, transparent); --select-border: var(--calm-accent);
    --scrollbar-track: transparent; --scrollbar-thumb: var(--calm-border-strong); --scrollbar-thumb-hover: var(--calm-fg-faint);

    --ws-side-w: clamp(220px, 22vw, 300px);

    position: fixed; inset: 0;
    display: flex; flex-direction: column;
    background: var(--calm-bg); color: var(--calm-fg);
    font-family: ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif;
    -webkit-font-smoothing: antialiased; overflow: hidden;
  }

  /* ===================== TOP BAR ===================== */
  .topbar { display: flex; align-items: center; gap: 0.6rem; padding: 0.4rem 0.6rem; background: var(--calm-surface); border-bottom: 1px solid var(--calm-border); flex: 0 0 auto; z-index: 30; }
  .top-left { display: flex; align-items: center; gap: 0.5rem; flex: 0 0 auto; }
  .top-center { flex: 1; min-width: 0; display: flex; }
  .top-right { display: flex; align-items: center; gap: 0.25rem; flex: 0 0 auto; }

  .brand { display: inline-flex; align-items: baseline; gap: 0.35rem; font-weight: 800; font-size: 1rem; letter-spacing: -0.01em; }
  .brand-dot { width: 9px; height: 9px; border-radius: 50%; align-self: center; background: linear-gradient(135deg, var(--calm-accent), var(--calm-accent-2)); box-shadow: 0 0 0 3px color-mix(in srgb, var(--calm-accent) 20%, transparent); }
  .brand-ver { font-size: 0.62rem; font-weight: 600; color: var(--calm-fg-faint); background: var(--calm-chip); padding: 0.1rem 0.4rem; border-radius: 6px; }

  .reg-toggle { display: inline-flex; align-items: center; gap: 0.3rem; padding: 0.3rem 0.55rem; background: var(--calm-chip); border: 1px solid var(--calm-border); color: var(--calm-fg-muted); border-radius: 7px; font-size: 0.74rem; font-weight: 600; cursor: pointer; white-space: nowrap; }
  .reg-toggle:hover { background: var(--calm-chip-hover); color: var(--calm-fg); }
  .reg-toggle.on { color: var(--calm-accent); }

  .overflow-menu, .user-menu, .ctx-menu { position: absolute; z-index: 80; min-width: 11rem; background: var(--calm-surface); border: 1px solid var(--calm-border-strong); border-radius: 8px; padding: 0.3rem; box-shadow: var(--calm-shadow-lg); display: flex; flex-direction: column; gap: 0.1rem; }
  .overflow-menu button, .user-menu .um-item, .ctx-menu .ctx-item { display: flex; align-items: center; gap: 0.4rem; padding: 0.45rem 0.55rem; border-radius: 6px; background: transparent; border: 0; color: var(--calm-fg); font: inherit; font-size: 0.82rem; text-align: left; cursor: pointer; width: 100%; }
  .overflow-menu button:hover, .user-menu .um-item:hover, .ctx-menu .ctx-item:hover:not(:disabled) { background: var(--calm-chip-hover); }
  .overflow-menu .danger, .user-menu .um-item.danger, .ctx-menu .ctx-item.danger { color: var(--calm-danger); }
  .ctx-menu .ctx-item:disabled { opacity: 0.4; cursor: not-allowed; }

  .icon-btn { width: 30px; height: 30px; display: inline-flex; align-items: center; justify-content: center; background: transparent; border: 1px solid transparent; color: var(--calm-fg-muted); border-radius: 6px; cursor: pointer; font-size: 0.88rem; font-weight: 600; transition: background 0.14s ease, color 0.14s ease; }
  .icon-btn:hover { background: var(--calm-chip-hover); color: var(--calm-fg); }
  .icon-btn.on { color: var(--calm-accent); background: color-mix(in srgb, var(--calm-accent) 14%, transparent); }
  .pill-btn { display: inline-flex; align-items: center; gap: 0.35rem; padding: 0.3rem 0.7rem; background: var(--calm-chip); border: 1px solid var(--calm-border); color: var(--calm-fg); border-radius: 8px; font-size: 0.78rem; font-weight: 600; cursor: pointer; white-space: nowrap; }
  .pill-btn:hover { background: var(--calm-chip-hover); }
  .pill-btn.accent { background: linear-gradient(135deg, var(--calm-accent), color-mix(in srgb, var(--calm-accent-2) 60%, var(--calm-accent))); color: #06121f; border-color: transparent; }
  .pill-btn.accent:hover { filter: brightness(1.06); }
  .divider-y { width: 1px; height: 20px; background: var(--calm-border); margin: 0 0.15rem; }

  .user-wrap, .overflow-wrap { position: relative; }
  .user-menu, .overflow-menu { top: calc(100% + 8px); right: 0; }
  .um-head { font-size: 0.62rem; text-transform: uppercase; letter-spacing: 0.08em; color: var(--calm-fg-faint); font-weight: 700; padding: 0.35rem 0.55rem 0.25rem; }

  /* ===================== BODY ===================== */
  .body { flex: 1; display: flex; min-height: 0; min-width: 0; position: relative; }

  .region { flex: 0 0 var(--ws-side-w); width: var(--ws-side-w); min-height: 0; overflow: hidden; }
  .rail-region { border-right: 1px solid var(--calm-border); }
  .ctx-region { border-left: 1px solid var(--calm-border); }
  .region.collapsed { flex: 0 0 38px; width: 38px; }

  .rail-host, .ctx-host { height: 100%; min-height: 0; overflow: hidden; display: flex; flex-direction: column; }
  .rail-host > :global(*), .ctx-host > :global(*) { flex: 1; min-height: 0; }
  .ctx-host { overflow: auto; }

  .center { flex: 1; min-width: 0; min-height: 0; display: flex; flex-direction: column; padding: 0.55rem; }

  .canvas { flex: 1; min-height: 0; min-width: 0; display: flex; flex-direction: column; background: var(--calm-surface); border: 1px solid var(--calm-border); border-radius: 8px; overflow: hidden; box-shadow: var(--calm-shadow-sm); }
  .canvas-head { flex: 0 0 auto; display: flex; align-items: center; justify-content: space-between; gap: 0.5rem; padding: 0.35rem 0.45rem 0.35rem 0.7rem; border-bottom: 1px solid var(--calm-border); background: var(--calm-surface-2); }
  .canvas-title { display: inline-flex; align-items: center; gap: 0.4rem; font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.06em; color: var(--calm-fg-muted); font-weight: 700; min-width: 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
  .canvas-glyph { color: var(--calm-accent-2); }
  .canvas-ctl { display: flex; align-items: center; gap: 0.1rem; flex: 0 0 auto; }
  .ctl { display: inline-flex; align-items: center; gap: 0.25rem; height: 26px; padding: 0 0.5rem; background: transparent; border: 1px solid transparent; color: var(--calm-fg-muted); border-radius: 6px; cursor: pointer; font-size: 0.76rem; font-weight: 600; white-space: nowrap; }
  .ctl:hover { background: var(--calm-chip-hover); color: var(--calm-fg); }
  .canvas-body { flex: 1; min-height: 0; min-width: 0; overflow: hidden; position: relative; background: var(--calm-bg); }
  .stage-grid { width: 100%; height: 100%; min-height: 0; min-width: 0; padding: 0.45rem; }

  /* region maximize */
  .region.hidden-by-max, .center.hidden-by-max { display: none; }
  .region.is-max { flex: 1; width: auto; }
  .region.is-max :global(.panel), .center.is-max .canvas { box-shadow: var(--calm-shadow-lg); }

  /* ===================== RESPONSIVE OVERLAYS ===================== */
  .scrim { position: fixed; inset: 0; z-index: 90; background: color-mix(in srgb, var(--calm-bg) 55%, transparent); border: 0; cursor: pointer; }
  .overlay-panel { position: fixed; top: 0; bottom: 0; z-index: 95; width: min(86vw, 340px); background: var(--calm-surface); box-shadow: var(--calm-shadow-lg); display: flex; flex-direction: column; }
  .overlay-panel.left { left: 0; border-right: 1px solid var(--calm-border-strong); }
  .overlay-panel.right { right: 0; border-left: 1px solid var(--calm-border-strong); }
  .overlay-panel :global(.panel) { height: 100%; }

  /* ===================== CONTEXT MENU ===================== */
  .ctx-menu { position: fixed; z-index: 1300; min-width: 12rem; }

  /* ===================== TOAST / BOOT / LOGIN / WIZARD ===================== */
  .toast { position: fixed; bottom: 1.1rem; left: 50%; transform: translateX(-50%); background: var(--calm-surface); color: var(--calm-fg); border: 1px solid var(--calm-border-strong); border-radius: 8px; padding: 0.45rem 1rem; font-size: 0.82rem; font-weight: 600; box-shadow: var(--calm-shadow-lg); z-index: 1100; }
  .boot { position: fixed; inset: 0; display: grid; place-items: center; background: var(--calm-bg); }
  .boot-mark { font-weight: 800; font-size: 1.3rem; color: var(--calm-accent); letter-spacing: -0.01em; opacity: 0.85; }
  .login { position: fixed; inset: 0; display: grid; place-items: center; background: var(--calm-bg); padding: 1rem; }
  .login-card { width: 100%; max-width: 30rem; background: var(--calm-surface); border: 1px solid var(--calm-border); border-radius: 10px; padding: 2rem; display: flex; flex-direction: column; gap: 0.7rem; box-shadow: var(--calm-shadow-lg); }
  .login-mark { font-weight: 800; font-size: 1.1rem; color: var(--calm-accent); }
  .login-card h1 { font-size: 1.5rem; margin: 0; }
  .login-card p { color: var(--calm-fg-muted); font-size: 0.88rem; margin: 0; }
  .login-card textarea { width: 100%; box-sizing: border-box; padding: 0.6rem 0.7rem; background: var(--calm-input); color: var(--calm-fg); border: 1px solid var(--calm-border); border-radius: 6px; font-family: ui-monospace, monospace; font-size: 0.82rem; resize: vertical; }
  .login-card textarea:focus { outline: none; border-color: var(--calm-accent); }
  .login-err { color: var(--calm-danger); font-size: 0.84rem; }
  .login-btn { padding: 0.6rem; border: 0; border-radius: 8px; cursor: pointer; background: linear-gradient(135deg, var(--calm-accent), var(--calm-accent-2)); color: #06121f; font-weight: 700; font-size: 0.92rem; }
  .login-btn:hover { filter: brightness(1.06); }
  .wizard-overlay { position: fixed; inset: 0; z-index: 1200; background: color-mix(in srgb, var(--calm-bg) 70%, transparent); backdrop-filter: blur(6px); display: flex; align-items: center; justify-content: center; padding: 2vh 2vw; }

  /* ===================== SCROLLBARS ===================== */
  .ws-root :global(*) { scrollbar-width: thin; scrollbar-color: var(--scrollbar-thumb) transparent; }
  .ws-root :global(*::-webkit-scrollbar) { width: 11px; height: 11px; }
  .ws-root :global(*::-webkit-scrollbar-track) { background: transparent; }
  .ws-root :global(*::-webkit-scrollbar-thumb) { background: var(--scrollbar-thumb); border-radius: 10px; border: 3px solid var(--calm-surface); }
  .ws-root :global(*::-webkit-scrollbar-thumb:hover) { background: var(--scrollbar-thumb-hover); }
</style>
