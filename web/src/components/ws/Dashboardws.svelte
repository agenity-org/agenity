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

  ONE GENERIC SPLIT-TREE — there are NO fixed roster/inspector regions. The
  whole body is a single recursive tree of GENERIC panes; agents are reached
  through a `sessions` pane (replacing the old roster). Every pane — including
  the former side regions — is uniformly drag-resizable, collapsible,
  maximizable AND content-changeable through its own content picker.

  · Pane content types: Sessions / Inspector / Terminal / Kanban / Events /
    Transcript / Mesh / Tasks / MCP Log. Click a pane to focus it; its picker
    switches what it shows.
  · Real data + live xterm — sessions/teams/memberships/events @2.5s +
    EventSource + WidgetTerminal; agentIdentity colors/icons everywhere; both
    --calm-* themes; user / sign-out menu (themed).
  · Right-click an AGENT (in a Sessions pane) → "Agent settings" (reuses v08
    AgentSettings). Right-click a TEAM → "Team settings" (reuses v08
    TeamSettings). Right-click a pane header → split/collapse/maximize/close.
  · ESC closes popups/menus/modals/overlays in priority order.
  · Settings (WsSettings) is config-only, reached from the 👤 user menu (no
    gear in the header). Font size lives there, under Appearance.

  Self-contained under components/ws/. Reuses calm/* + v08 components read-only.
  Owns its own --calm-* tokens (two full palettes) so it never collides.
-->
<script>
  import { onMount } from 'svelte';
  import '@xterm/xterm/css/xterm.css';
  import WsPane from './WsPane.svelte';
  import WsStrip from './WsStrip.svelte';
  import WsSettings from './WsSettings.svelte';
  import AgentSettings from '../v08/AgentSettings.svelte';
  import TeamSettings from '../v08/TeamSettings.svelte';
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
  // Per-workspace MRU of TERMINAL leaf ids the operator has focused, newest
  // first. Used so that clicking an agent in a Sessions pane rebinds the
  // last terminal the operator actually touched — NOT an arbitrary/first one.
  // (Clicking a Sessions row moves focus to the Sessions leaf on mousedown,
  // so focusedLeafId is no longer the terminal by the time we rebind.)
  let wsTermMru = $state({}); // workspaceId -> [terminalLeafId, …]
  let persistReady = $state(false);

  let activeWs = $derived(workspaces.find((w) => w.id === activeId) || null);
  let focusedLeafId = $derived(activeWs ? (wsFocus[activeWs.id] || '') : '');
  let maximizedLeafId = $derived(activeWs ? (wsMax[activeWs.id] || '') : '');

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

  // ---- saved views (server-side, durable named snapshots) ----
  // The `workspaces` array above is the live working set (auto-persisted to
  // localStorage). A SAVED VIEW is a named snapshot of that array PUT to the
  // server (GET/PUT /api/v1/workspaces[/{name}]) so it survives a browser
  // clear + is reachable cross-device. Loading a view replaces the working
  // set + re-persists it to localStorage.
  let viewsMenuOpen = $state(false);   // the "Views" dropdown
  let savedViews = $state([]);         // [name, …] from GET /api/v1/workspaces
  let viewsBusy = $state(false);       // a save/load is in flight
  let savingView = $state(false);      // the inline "name this view" input is showing
  let saveViewName = $state('');       // bound to that input

  // ---- agent / team editors (reused v08, opened from context menus) ----
  let editAgent = $state(null);        // session object | null
  let editTeam = $state(null);         // team object | null

  // ---- responsive ----
  let narrow = $state(false);
  let tight = $state(false);

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
      // A successful poll means the daemon + token are healthy again, so let a
      // previously-capped SSE reconnect (transient-outage self-heal). The
      // backoff + the needLogin/token guard in startEventStream prevent this
      // from reintroducing a tight reconnect loop.
      evRetries = 0;
      sessions = s.sessions || [];
      registerRoster(
        [...sessions]
          .sort((a, b) => (a.created_at || '').localeCompare(b.created_at || '') || (a.name || '').localeCompare(b.name || ''))
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
        if (w) {
          selectedAgent = w.name;
          // Only bind into an EXISTING terminal pane. Never split a new
          // terminal as a side effect of a background refresh — a
          // terminal-less workspace (seeded "Board" / "Talk", or any
          // operator-composed one) must stay exactly as the operator left it.
          if (activeWs && firstTerminalLeafId(activeWs.layout)) bindFocusedTerminal(w.name, false);
        }
      }
    } catch {}
  }

  // Lightweight pollers for mesh + tasks panes (only fetched while present).
  let hasMeshPane = $derived(workspaces.some((w) => T.leaves(w.layout).some((l) => l.widget === 'mesh')));
  let hasTasksPane = $derived(workspaces.some((w) => T.leaves(w.layout).some((l) => l.widget === 'tasks')));
  async function loadMesh() { try { const r = await fetch(`${API}/peers`); if (r.ok) { const j = await r.json(); peers = j.peers || []; } } catch {} }
  async function loadTasks() { try { const r = await fetch(`${API}/tasks`); if (r.ok) { const j = await r.json(); tasks = j.tasks || []; } } catch {} }

  let evStream = null;
  let evRetries = 0;
  let evRetryTimer = null;   // queued bounded-retry setTimeout id (so we can cancel it)
  function startEventStream() {
    // No-op while signed out: a queued retry must NOT revive the stream after
    // sign-out/unmount (its `if (evStream) return` guard is null then, so it
    // would re-open and reflood). needLogin / no-token short-circuits that.
    if (needLogin || !getToken()) return;
    if (evStream) return;
    const tok = getToken();
    const q = tok ? '?token=' + encodeURIComponent(tok) : '';
    try {
      evStream = new EventSource(`${API}/events/stream${q}`);
      evStream.onopen = () => { evRetries = 0; };
      evStream.onmessage = (e) => { try { events = [...events, JSON.parse(e.data)].slice(-200); } catch {} };
      // BOUNDED backoff: the 2.5s poll already refreshes events, so a
      // misbehaving/expired-token SSE endpoint must NOT infinite-reconnect —
      // that floods the console + hammers the daemon. Cap at 4 tries, then
      // fall back to polling only. The retry timer id is stored so
      // stopLiveLoop() can clearTimeout it (else a queued retry fires after
      // sign-out and re-opens the stream, reviving the flood the cap kills).
      evStream.onerror = () => {
        evStream?.close(); evStream = null;
        if (evRetries < 4) { evRetries += 1; evRetryTimer = setTimeout(startEventStream, Math.min(30000, 3000 * evRetries)); }
      };
    } catch {}
  }

  // ---- live loop (single idempotent owner of polling + 401 + EventSource) ----
  // Called from BOTH onMount's token-present branch AND doLogin success, so a
  // first-run token login starts the roster/status/live-count polling without a
  // hard reload. Idempotent: re-entry while already running is a no-op.
  let pollIv = null;
  let on401 = null;
  function startLiveLoop() {
    if (pollIv) return;           // already running — don't double-start
    refresh();
    startEventStream();
    pollIv = setInterval(refresh, 2500);
    on401 = () => { needLogin = true; };
    window.addEventListener('chepherd-401', on401);
  }
  function stopLiveLoop() {
    if (pollIv) { clearInterval(pollIv); pollIv = null; }
    if (on401) { window.removeEventListener('chepherd-401', on401); on401 = null; }
    if (evRetryTimer) { clearTimeout(evRetryTimer); evRetryTimer = null; }   // kill any queued SSE retry
    try { evStream?.close(); } catch {}
    evStream = null;
  }

  // ---------------- active-workspace layout helpers ----------------
  function curLayout() { return activeWs?.layout || null; }
  function setCurLayout(next) {
    if (!activeWs) return;
    workspaces = workspaces.map((w) => (w.id === activeWs.id ? { ...w, layout: next } : w));
  }
  function setFocusedLeaf(id) {
    if (!activeWs) return;
    wsFocus = { ...wsFocus, [activeWs.id]: id };
    // If a TERMINAL leaf just got focus, remember it as most-recent for this
    // workspace so a later Sessions-row click rebinds THIS terminal.
    const leaf = T.findLeaf(activeWs.layout, id);
    if (leaf?.widget === 'terminal') pushTermMru(activeWs.id, id);
  }
  function setMaxLeaf(id) { if (activeWs) wsMax = { ...wsMax, [activeWs.id]: id }; }

  function pushTermMru(wsId, leafId) {
    if (!wsId || !leafId) return;
    const prev = wsTermMru[wsId] || [];
    wsTermMru = { ...wsTermMru, [wsId]: [leafId, ...prev.filter((x) => x !== leafId)].slice(0, 12) };
  }
  // Resolve the terminal leaf a Sessions-click should rebind, in priority:
  //   1. the currently-focused leaf IF it is itself a terminal;
  //   2. else the most-recently-focused terminal leaf that still exists;
  //   3. else the first terminal in the tree.
  // Returns '' when the workspace has no terminal pane at all.
  function mruTerminalLeafId(layout, focusedId) {
    const focused = T.findLeaf(layout, focusedId);
    if (focused && focused.widget === 'terminal') return focused.id;
    const mru = activeWs ? (wsTermMru[activeWs.id] || []) : [];
    for (const id of mru) {
      const l = T.findLeaf(layout, id);
      if (l && l.widget === 'terminal') return l.id;
    }
    return firstTerminalLeafId(layout);
  }

  function firstTerminalLeafId(layout) { const tl = T.leaves(layout).find((l) => l.widget === 'terminal'); return tl ? tl.id : ''; }
  function ensureFocusedLeaf(layout) { const all = T.leaves(layout); if (!all.find((l) => l.id === focusedLeafId)) setFocusedLeaf(all[0]?.id || ''); }

  function selectAgent(name) {
    selectedAgent = name;
    pushMRU(name);
    bindFocusedTerminal(name, true);
  }

  // Clicking an agent (in a Sessions pane) binds it to a terminal:
  //   1. if the focused pane IS a terminal → rebind THAT exact leaf;
  //   2. else the most-recently-focused terminal leaf (MRU) → rebind it;
  //   3. else the first terminal in the tree → rebind it;
  //   4. else split a NEW terminal off the focused pane (never clobber a
  //      Sessions / inspector / kanban pane by converting it).
  // Note: a Sessions-row click moves focus to the Sessions leaf on mousedown,
  // so by the time we land here focusedLeafId is the Sessions pane, NOT the
  // terminal the operator last touched — hence the MRU lookup in (2). Without
  // it we'd fall through to firstTerminalLeafId and rebind an arbitrary
  // terminal (the reported multi-terminal mis-bind bug).
  function bindFocusedTerminal(name, flashIt) {
    if (!activeWs) return;
    let layout = activeWs.layout;
    ensureFocusedLeaf(layout);
    const fid = wsFocus[activeWs.id] || T.leaves(layout)[0]?.id || '';

    let targetId = mruTerminalLeafId(layout, fid);
    if (targetId) {
      layout = T.setLeafConfig(layout, targetId, { agent: name });
      wsFocus = { ...wsFocus, [activeWs.id]: targetId };
      pushTermMru(activeWs.id, targetId);
      if (flashIt) flash(`Focus → ${name}`);
    } else {
      // No terminal anywhere — split a new one off the focused pane so the
      // Sessions list (or whatever is focused) is preserved.
      const base = fid || T.leaves(layout)[0]?.id;
      if (base) {
        const { tree, newId } = T.splitLeaf(layout, base, 'h', 'terminal', { agent: name });
        layout = tree;
        wsFocus = { ...wsFocus, [activeWs.id]: newId };
        pushTermMru(activeWs.id, newId);
      } else {
        layout = T.leaf('terminal', { agent: name });
        const onlyId = T.leaves(layout)[0].id;
        wsFocus = { ...wsFocus, [activeWs.id]: onlyId };
        pushTermMru(activeWs.id, onlyId);
      }
      if (flashIt) flash(`Opened ${name}`);
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

  // ---------------- saved views (server-side, durable) ----------------
  // Snapshot the live `workspaces` array (the full set) so the server stores
  // exactly what loadWorkspaces consumes — same {id,name,layout} shape.
  function viewSnapshot() {
    return workspaces.map((w) => ({ id: w.id, name: w.name, layout: w.layout }));
  }
  async function loadSavedViewNames() {
    try {
      const r = await fetch(`${API}/workspaces`);
      if (!r.ok) { savedViews = []; return; }
      const j = await r.json();
      savedViews = Array.isArray(j.workspaces) ? j.workspaces : [];
    } catch { savedViews = []; }
  }
  function beginSaveView() {
    // Default the name to the active workspace's, so a one-tap save is sane.
    saveViewName = (activeWs?.name || `View ${savedViews.length + 1}`);
    savingView = true;
  }
  function cancelSaveView() { savingView = false; saveViewName = ''; }
  async function commitSaveView() {
    const name = (saveViewName || '').trim();
    if (!name) { flash('Name the view first'); return; }
    viewsBusy = true;
    try {
      const r = await fetch(`${API}/workspaces/${encodeURIComponent(name)}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(viewSnapshot()),
      });
      if (!r.ok) { flash(`Save failed (${r.status})`); return; }
      flash(`Saved view “${name}”`);
      savingView = false; saveViewName = '';
      await loadSavedViewNames();
    } catch (e) {
      flash('Save failed');
    } finally {
      viewsBusy = false;
    }
  }
  async function loadSavedView(name) {
    viewsBusy = true;
    try {
      const r = await fetch(`${API}/workspaces/${encodeURIComponent(name)}`);
      if (!r.ok) { flash(`Load failed (${r.status})`); return; }
      const blob = await r.json();
      const next = T.sanitizeWorkspaceList(blob);
      if (!next || !next.length) { flash('View is empty / unreadable'); return; }
      // Replace the working set, reseed per-workspace focus, persist locally.
      workspaces = next;
      const focusSeed = {};
      for (const w of workspaces) focusSeed[w.id] = T.leaves(w.layout)[0]?.id || '';
      wsFocus = focusSeed;
      wsMax = {};
      activeId = workspaces[0].id;
      T.saveWorkspaces(viewSnapshot());
      T.saveActiveId(activeId);
      viewsMenuOpen = false;
      flash(`Loaded view “${name}”`);
    } catch (e) {
      flash('Load failed');
    } finally {
      viewsBusy = false;
    }
  }
  function toggleViewsMenu(e) {
    e.stopPropagation();
    closeAllMenus();
    viewsMenuOpen = !viewsMenuOpen;
    if (viewsMenuOpen) { savingView = false; saveViewName = ''; loadSavedViewNames(); }
  }

  // ---------------- agent / team editors (reuse v08 modals) ----------------
  function openAgentSettings(name) {
    const s = sessions.find((x) => x.name === name);
    if (!s) { flash(`No session “${name}”`); return; }
    closeAllMenus();
    editTeam = null;
    editAgent = s;
  }
  function openTeamSettings(teamName) {
    const t = teams.find((x) => (x.name || x) === teamName) || { name: teamName, topology: 'hub' };
    closeAllMenus();
    editAgent = null;
    editTeam = t;
  }
  let editTeamMembers = $derived(
    editTeam ? memberships.filter((m) => (m.team || m.team_name) === editTeam.name) : []
  );

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
    startLiveLoop();
  }
  function signOut() {
    userMenuOpen = false;
    try { localStorage.removeItem('chepherd-token'); } catch {}
    stopLiveLoop();
    needLogin = true;
  }

  // ---------------- context menus (#6) ----------------
  function openWsMenu(w, x, y) {
    closeAllMenus();
    clearPendingStop();   // a new menu cancels any stale Stop confirm
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
    clearPendingStop();   // a new menu cancels any stale Stop confirm
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
  // Right-click an AGENT row (in any Sessions pane) → operator LIFECYCLE
  // actions + settings (Pause/Resume, Restart, Stop, Agent settings). Mirrors
  // the inline pause/stop pattern in WsLeaf (same endpoints) so the right-click
  // menu and the row buttons act identically.
  let agentBusy = $state('');          // name currently in flight (any lifecycle verb)
  let agentPendingStop = $state('');   // inline two-click confirm for destructive stop
  let agentPendingStopTimer = null;    // the 4s confirm-expiry timer (cancellable)

  // Clear any in-flight Stop confirm + its expiry timer. Called whenever a NEW
  // context menu opens, so a stale Stop timer from a previous agent can never
  // blind-null a freshly-opened menu for a DIFFERENT agent.
  function clearPendingStop() {
    if (agentPendingStopTimer) { clearTimeout(agentPendingStopTimer); agentPendingStopTimer = null; }
    agentPendingStop = '';
  }

  async function agentLifecycle(name, kind, paused) {
    if (agentBusy === name) return;
    agentBusy = name;
    let url, method = 'POST', body = null;
    if (kind === 'pause') { url = `${API}/sessions/${name}/pause`; body = JSON.stringify({ paused }); }
    else if (kind === 'restart') { url = `${API}/sessions/${name}/restart`; }
    else if (kind === 'stop') { url = `${API}/sessions/${name}`; method = 'DELETE'; }
    else { agentBusy = ''; return; }
    try {
      // Only flash success on a 2xx. A non-PTY external peer or an orphan row
      // 404s (or 4xx/5xx); flashing "Stopped X" then would be a FALSE success.
      const r = await fetch(url, { method, headers: body ? { 'Content-Type': 'application/json' } : {}, body });
      if (r.ok) {
        flash(kind === 'pause' ? (paused ? `Paused ${name}` : `Resumed ${name}`) : kind === 'restart' ? `Restarting ${name}` : `Stopped ${name}`);
      } else {
        const verb = kind === 'pause' ? (paused ? 'pause' : 'resume') : kind;
        flash(`Could not ${verb} ${name} (${r.status})`);
      }
    } catch (e) {
      flash(`Could not ${kind === 'pause' ? (paused ? 'pause' : 'resume') : kind} ${name}`);
    }
    agentBusy = '';
    refresh();
  }

  function openAgentMenu(name, x, y) {
    closeAllMenus();
    // Opening this menu for a DIFFERENT agent cancels any pending Stop confirm
    // (and its 4s timer) belonging to the previous agent — so X's stale timer
    // can never close Y's menu. The re-entrant confirm re-open passes the SAME
    // name (pending already set), so it preserves it.
    if (agentPendingStop && agentPendingStop !== name) clearPendingStop();
    const s = sessions.find((x2) => x2.name === name);
    const paused = !!s?.paused;
    const stopConfirming = agentPendingStop === name;
    // A non-PTY external A2A peer / exited row has no controllable container —
    // its lifecycle endpoints 404. Disable those verbs so the operator can't
    // trigger a guaranteed-failing action (Agent settings stays enabled).
    const noLifecycle = !s || s.exited || s.agent === 'external-a2a' || s.external;
    ctxMenu = {
      x, y,
      items: [
        paused
          ? { label: 'Resume', disabled: noLifecycle || agentBusy === name, onpick: () => agentLifecycle(name, 'pause', false) }
          : { label: 'Pause', disabled: noLifecycle || agentBusy === name, onpick: () => agentLifecycle(name, 'pause', true) },
        { label: 'Restart', disabled: noLifecycle || agentBusy === name, onpick: () => agentLifecycle(name, 'restart') },
        {
          label: stopConfirming ? 'Click again to stop' : 'Stop',
          danger: true,
          disabled: noLifecycle || agentBusy === name,
          keepOpen: !stopConfirming,   // first click re-opens the menu to confirm
          onpick: () => {
            if (!stopConfirming) {
              // Cancel any previous confirm timer before starting this one, then
              // scope the expiry: only auto-close the menu if THIS agent is
              // still the pending one (a different menu opening resets it).
              clearPendingStop();
              agentPendingStop = name;
              agentPendingStopTimer = setTimeout(() => {
                agentPendingStopTimer = null;
                if (agentPendingStop === name) { agentPendingStop = ''; if (ctxMenu) ctxMenu = null; }
              }, 4000);
              openAgentMenu(name, x, y);   // reopen with the confirm label
              return;
            }
            clearPendingStop();
            agentLifecycle(name, 'stop');
          },
        },
        { label: 'Agent settings', onpick: () => openAgentSettings(name) },
      ],
    };
  }
  // Right-click a TEAM label (in any Sessions pane) → Team settings (#11).
  function openTeamMenu(teamName, x, y) {
    closeAllMenus();
    clearPendingStop();   // a new menu cancels any stale Stop confirm
    ctxMenu = {
      x, y,
      items: [
        { label: 'Team settings', onpick: () => openTeamSettings(teamName) },
      ],
    };
  }
  function pickCtx(item) {
    if (item.disabled) return;
    // Most items dismiss the menu; a `keepOpen` item (e.g. the first click of
    // the two-click Stop confirm) handles re-rendering the menu itself.
    if (!item.keepOpen) ctxMenu = null;
    item.onpick();
  }
  function closeAllMenus() { userMenuOpen = false; overflowOpen = false; viewsMenuOpen = false; ctxMenu = null; }

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
    // ESC priority order: menus → modals → rename → maximize.
    if (ctxMenu) { ctxMenu = null; return; }
    if (editAgent) { editAgent = null; return; }
    if (editTeam) { editTeam = null; return; }
    if (showWizard) { showWizard = false; refresh(); return; }
    if (showSettings) { showSettings = false; return; }
    if (savingView) { cancelSaveView(); return; }
    if (userMenuOpen || overflowOpen || viewsMenuOpen) { closeAllMenus(); return; }
    if (renamingId) { cancelRename(); return; }
    if (maximizedLeafId) { setMaxLeaf(''); return; }
  }
  // Any click reaching the window closes the open popups. The toggle buttons +
  // the items inside the menus all stopPropagation, so a click that bubbles
  // this far is necessarily OUTSIDE them → dismiss the context menu AND the
  // user / overflow menus (ESC is handled separately in onKey).
  function onGlobalClick() {
    if (ctxMenu) ctxMenu = null;
    if (userMenuOpen) userMenuOpen = false;
    if (overflowOpen) overflowOpen = false;
    // Don't auto-close the Views menu while its inline save-name input is
    // active (clicks inside it stopPropagation, but a focus/blur shouldn't
    // wipe a half-typed name); only the menu's own controls close it.
    if (viewsMenuOpen && !savingView) viewsMenuOpen = false;
  }

  // ---------------- responsive ----------------
  function applyResponsive() {
    try {
      const w = window.innerWidth;
      narrow = w < 980;
      tight = w < 720;
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

    // Single teardown for BOTH branches: always remove the chrome listeners
    // set up above, and stop the live loop (a no-op if it never started). This
    // closes over no later-declared consts, so the no-token branch can return
    // it safely (no TDZ, no leaked resize/keydown/click listeners).
    function cleanup() {
      window.removeEventListener('resize', applyResponsive);
      window.removeEventListener('keydown', onKey);
      window.removeEventListener('click', onGlobalClick);
      stopLiveLoop();
    }

    // With a token present, start polling + 401 + EventSource. Without one we
    // show the login screen; doLogin() starts the live loop on success.
    if (getToken()) startLiveLoop();
    else needLogin = true;

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
        <!-- #5 no A−/A+ in header (font lives in Settings → Appearance).
             #9 no gear in header (Settings reached from 👤 menu).
             #12 no +Pane in header (panes added by splitting + per-pane picker). -->
        {#if tight}
          <div class="overflow-wrap">
            <button class="icon-btn" onclick={(e) => { e.stopPropagation(); overflowOpen = !overflowOpen; if (overflowOpen) { savingView = false; saveViewName = ''; loadSavedViewNames(); } }} title="More" aria-label="More controls">⋯</button>
            {#if overflowOpen}
              <div class="user-menu" role="menu" onclick={(e) => { if (savingView) e.stopPropagation(); }} onkeydown={() => {}} tabindex="-1">
                <button class="um-item" role="menuitem" onclick={(e) => { e.stopPropagation(); overflowOpen = false; showWizard = true; }}>✦ Spawn agents</button>
                <button class="um-item" role="menuitem" onclick={(e) => { e.stopPropagation(); toggleTheme(); }}>{theme === 'dark' ? '☀ Light theme' : '☾ Dark theme'}</button>
                <div class="um-sep"></div>
                <div class="um-head">Views</div>
                {#if savingView}
                  <div class="view-save-row">
                    <input
                      class="view-save-input"
                      type="text"
                      placeholder="View name…"
                      bind:value={saveViewName}
                      onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); commitSaveView(); } else if (e.key === 'Escape') { e.preventDefault(); cancelSaveView(); } }}
                      aria-label="View name"
                    />
                    <button class="view-save-go" disabled={viewsBusy} onclick={(e) => { e.stopPropagation(); commitSaveView(); }} title="Save view">Save</button>
                  </div>
                {:else}
                  <button class="um-item" role="menuitem" onclick={(e) => { e.stopPropagation(); loadSavedViewNames(); beginSaveView(); }}>＋ Save view…</button>
                {/if}
                {#if savedViews.length === 0}
                  <div class="views-empty">{viewsBusy ? 'Loading…' : 'No saved views'}</div>
                {:else}
                  {#each savedViews as v (v)}
                    <button class="um-item" role="menuitem" disabled={viewsBusy} onclick={(e) => { e.stopPropagation(); overflowOpen = false; loadSavedView(v); }} title={`Load “${v}”`}>▸ {v}</button>
                  {/each}
                {/if}
                <div class="um-sep"></div>
                <div class="um-head">Signed in</div>
                <button class="um-item" role="menuitem" onclick={(e) => { e.stopPropagation(); overflowOpen = false; showSettings = true; }}>⚙ Settings</button>
                <button class="um-item danger" role="menuitem" onclick={(e) => { e.stopPropagation(); signOut(); }}>⎋ Sign out</button>
              </div>
            {/if}
          </div>
        {:else}
          <button class="pill-btn accent" onclick={() => (showWizard = true)} title="Spawn agents">✦ Spawn</button>
          <div class="views-wrap">
            <button class="pill-btn" onclick={toggleViewsMenu} title="Save / load named views (durable, cross-device)" aria-haspopup="menu" aria-expanded={viewsMenuOpen}>▦ Views</button>
            {#if viewsMenuOpen}
              <div class="user-menu views-menu" role="menu" tabindex="-1" onclick={(e) => e.stopPropagation()} onkeydown={() => {}}>
                <div class="um-head">Save current</div>
                {#if savingView}
                  <div class="view-save-row">
                    <input
                      class="view-save-input"
                      type="text"
                      placeholder="View name…"
                      bind:value={saveViewName}
                      onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); commitSaveView(); } else if (e.key === 'Escape') { e.preventDefault(); cancelSaveView(); } }}
                      aria-label="View name"
                    />
                    <button class="view-save-go" disabled={viewsBusy} onclick={commitSaveView} title="Save view">Save</button>
                  </div>
                {:else}
                  <button class="um-item" role="menuitem" onclick={beginSaveView}>＋ Save view…</button>
                {/if}
                <div class="um-sep"></div>
                <div class="um-head">Saved views</div>
                {#if savedViews.length === 0}
                  <div class="views-empty">{viewsBusy ? 'Loading…' : 'No saved views yet'}</div>
                {:else}
                  {#each savedViews as v (v)}
                    <button class="um-item" role="menuitem" disabled={viewsBusy} onclick={() => loadSavedView(v)} title={`Load “${v}”`}>▸ {v}</button>
                  {/each}
                {/if}
              </div>
            {/if}
          </div>
          <button class="icon-btn" onclick={toggleTheme} title="Toggle light / dark" aria-label="Toggle theme">{theme === 'dark' ? '☀' : '☾'}</button>
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

    <!-- ===================== BODY — ONE GENERIC SPLIT-TREE ===================== -->
    <!-- No fixed roster/inspector regions. The whole desktop is a single
         recursive tree of generic, resizable, content-changeable panes. -->
    <main class="center">
      <div class="canvas-body">
        {#if activeWs}
          {#key activeWs.id}
            <div class="stage-grid">
              <WsPane
                node={maximizedLeafId && T.findLeaf(activeWs.layout, maximizedLeafId) ? T.findLeaf(activeWs.layout, maximizedLeafId) : activeWs.layout}
                {sessions} {teams} {memberships} {events} {peers} {tasks} {focusedSession} {selectedAgent}
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
                onpickagent={selectAgent}
                onopenagentnew={openAgentInNewPane}
                onagentctxmenu={openAgentMenu}
                onteamctxmenu={openTeamMenu}
                canClose={T.countLeaves(activeWs.layout) > 1}
              />
            </div>
          {/key}
        {/if}
      </div>
    </main>

    <!-- ===================== CONTEXT MENU ===================== -->
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

    <!-- #11 reuse v08 editors, opened from agent/team right-click menus -->
    {#if editAgent}
      <AgentSettings agent={editAgent} {teams} onClose={() => { editAgent = null; refresh(); }} />
    {/if}
    {#if editTeam}
      <TeamSettings team={editTeam} members={editTeamMembers} onClose={() => (editTeam = null)} onChanged={refresh} />
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

  /* #10 — user / overflow menus fully tokened so they match light AND dark. */
  .user-menu, .ctx-menu { position: absolute; z-index: 80; min-width: 11rem; background: var(--calm-surface); color: var(--calm-fg); border: 1px solid var(--calm-border-strong); border-radius: 8px; padding: 0.3rem; box-shadow: var(--calm-shadow-lg); display: flex; flex-direction: column; gap: 0.1rem; }
  .user-menu .um-item, .ctx-menu .ctx-item { display: flex; align-items: center; gap: 0.4rem; padding: 0.45rem 0.55rem; border-radius: 6px; background: transparent; border: 0; color: var(--calm-fg); font: inherit; font-size: 0.82rem; text-align: left; cursor: pointer; width: 100%; }
  .user-menu .um-item:hover, .ctx-menu .ctx-item:hover:not(:disabled) { background: var(--calm-chip-hover); }
  .user-menu .um-item.danger, .ctx-menu .ctx-item.danger { color: var(--calm-danger); }
  .user-menu .um-item.danger:hover { background: color-mix(in srgb, var(--calm-danger) 14%, transparent); }
  .um-sep { height: 1px; background: var(--calm-border); margin: 0.25rem 0.2rem; }
  .ctx-menu .ctx-item:disabled { opacity: 0.4; cursor: not-allowed; }

  .icon-btn { width: 30px; height: 30px; display: inline-flex; align-items: center; justify-content: center; background: transparent; border: 1px solid transparent; color: var(--calm-fg-muted); border-radius: 6px; cursor: pointer; font-size: 0.88rem; font-weight: 600; transition: background 0.14s ease, color 0.14s ease; }
  .icon-btn:hover { background: var(--calm-chip-hover); color: var(--calm-fg); }
  .icon-btn.on { color: var(--calm-accent); background: color-mix(in srgb, var(--calm-accent) 14%, transparent); }
  .pill-btn { display: inline-flex; align-items: center; gap: 0.35rem; padding: 0.3rem 0.7rem; background: var(--calm-chip); border: 1px solid var(--calm-border); color: var(--calm-fg); border-radius: 8px; font-size: 0.78rem; font-weight: 600; cursor: pointer; white-space: nowrap; }
  .pill-btn:hover { background: var(--calm-chip-hover); }
  .pill-btn.accent { background: linear-gradient(135deg, var(--calm-accent), color-mix(in srgb, var(--calm-accent-2) 60%, var(--calm-accent))); color: #06121f; border-color: transparent; }
  .pill-btn.accent:hover { filter: brightness(1.06); }
  .divider-y { width: 1px; height: 20px; background: var(--calm-border); margin: 0 0.15rem; }

  .user-wrap, .overflow-wrap, .views-wrap { position: relative; }
  .user-menu { top: calc(100% + 8px); right: 0; }
  .views-menu { min-width: 14rem; max-height: 60vh; overflow-y: auto; }
  .views-empty { padding: 0.5rem 0.55rem; color: var(--calm-fg-faint); font-size: 0.78rem; }
  .view-save-row { display: flex; align-items: center; gap: 0.35rem; padding: 0.25rem 0.3rem 0.4rem; }
  .view-save-input { flex: 1; min-width: 0; padding: 0.35rem 0.5rem; background: var(--calm-input); color: var(--calm-fg); border: 1px solid var(--calm-border); border-radius: 6px; font: inherit; font-size: 0.8rem; }
  .view-save-input:focus { outline: none; border-color: var(--calm-accent); }
  .view-save-go { flex: 0 0 auto; padding: 0.35rem 0.6rem; background: var(--calm-accent); color: #06121f; border: 0; border-radius: 6px; font-size: 0.78rem; font-weight: 700; cursor: pointer; }
  .view-save-go:hover:not(:disabled) { filter: brightness(1.06); }
  .view-save-go:disabled { opacity: 0.5; cursor: progress; }
  .user-menu .um-item:disabled { opacity: 0.5; cursor: progress; }
  .um-head { font-size: 0.62rem; text-transform: uppercase; letter-spacing: 0.08em; color: var(--calm-fg-faint); font-weight: 700; padding: 0.35rem 0.55rem 0.25rem; }

  /* ===================== BODY — ONE GENERIC TREE ===================== */
  .center { flex: 1; min-width: 0; min-height: 0; display: flex; flex-direction: column; padding: 0.45rem; }
  .canvas-body { flex: 1; min-height: 0; min-width: 0; overflow: hidden; position: relative; background: var(--calm-bg); }
  .stage-grid { width: 100%; height: 100%; min-height: 0; min-width: 0; }

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
