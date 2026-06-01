<!--
  v0.8 Workspace canvas — recursive tree of splits + widget catalog.
  Refs #80, #85.

  Layout = Pane | HSplit{a,b,ratio} | VSplit{a,b,ratio}
  Pane    = { id, widget, config }

  Widgets: terminal | session-list | session-board | identity-card |
  location-card | process-card | shepherd-assessment-card | inbox |
  events | scorecard-aggregate | canon-viewer

  Loaded from / saved to /api/v1/workspaces/<name> (shared across operators).
-->
<script>
  import { onMount } from 'svelte';
  import '@xterm/xterm/css/xterm.css';
  import Pane from './Pane.svelte';
  import SpawnWizard from './SpawnWizard.svelte';
  import SpawnWizardV9 from '../v09/SpawnWizardV9.svelte';
  import AgentSettings from './AgentSettings.svelte';
  import TeamSettings from './TeamSettings.svelte';

  // #223 — version label shown in the topbar brand badge. Passed by
  // each /vMAJOR.MINOR.PATCH/ Astro route so legacy /v0.8/ stays
  // "v0.8" while the v0.9.X routes display the right release. Default
  // matches the historical hardcoded value so any caller that doesn't
  // pass the prop renders identically to pre-#223.
  let { version = 'v0.8' } = $props();

  // #157 — install fetch-auth wrapper at module top-level, BEFORE any child
  // widget's onMount fires. (Previously it lived inside Workspace.onMount,
  // which runs AFTER children mount — so the first claude-status / sessions
  // fetch bypassed the wrapper and 401'd.) Guard with __chepherdFetchPatched
  // and an SSR-safe window check.
  if (typeof window !== 'undefined' && !window.__chepherdFetchPatched) {
    window.__chepherdFetchPatched = true;
    const _origFetch = window.fetch.bind(window);
    window.fetch = (input, init) => {
      const url = typeof input === 'string' ? input : (input?.url || '');
      if (url.startsWith('/api/')) {
        let tok = '';
        try { tok = localStorage.getItem('chepherd-token') || ''; } catch {}
        init = init || {};
        init.headers = new Headers(init.headers || (typeof input !== 'string' ? input.headers : undefined));
        if (tok && !init.headers.has('Authorization')) {
          init.headers.set('Authorization', 'Bearer ' + tok);
        }
        return _origFetch(input, init).then(r => {
          if (r.status === 401) {
            try { window.dispatchEvent(new CustomEvent('chepherd-401')); } catch {}
          }
          return r;
        });
      }
      return _origFetch(input, init);
    };
  }

  // --- clickOutside action (closes dropdowns when clicking elsewhere) ---
  function clickOutside(node, handler) {
    function handle(e) { if (!node.contains(e.target)) handler(); }
    document.addEventListener('click', handle, true);
    return { destroy() { document.removeEventListener('click', handle, true); } };
  }

  // --- props / state ---
  let sessions = $state([]);
  let teams = $state([]);
  let memberships = $state([]);
  let inbox = $state([]);
  let events = $state([]);
  let selectedAgent = $state(null);
  let theme = $state('dark');
  let showWizard = $state(false);
  let showAgentSettings = $state(false);
  let showTeamSettings = $state(null); // null | { team, members }
  let confirmDialog = $state(null);

  // Workspace layout — default = Focus template
  let layout = $state(defaultFocusLayout());

  function defaultFocusLayout() {
    // Goal: terminal gets generous middle column, agent-details gets
    // enough horizontal room on the right (≥ 280px in typical widths)
    // so the dense KV rows don't wrap. Left session-list 16%, terminal
    // column 56%, details column 28% — totals 100%.
    return {
      kind: 'h',
      ratio: 0.16,
      a: { kind: 'pane', id: 'p1', widget: 'session-list', config: {} },
      b: {
        kind: 'h', ratio: 0.68,
        a: {
          kind: 'v', ratio: 0.62,
          // #361 — center column: terminal up top, then a 3-way
          // horizontal band of federation / a2a-inbox / multi-host
          // BELOW it so the v0.9.3 cross-instance surfaces are
          // operator-visible on fresh /v0.9.3/ load.
          a: { kind: 'pane', id: 'p2', widget: 'terminal', config: {} },
          b: {
            kind: 'h', ratio: 0.34,
            a: { kind: 'pane', id: 'p_fed', widget: 'federation', config: {} },
            b: {
              kind: 'h', ratio: 0.5,
              a: { kind: 'pane', id: 'p_a2a', widget: 'a2a-inbox', config: {} },
              b: { kind: 'pane', id: 'p_mh', widget: 'multi-host', config: {} },
            },
          },
        },
        b: {
          kind: 'v', ratio: 0.5,
          a: { kind: 'pane', id: 'p4', widget: 'agent-details', config: {} },
          b: {
            kind: 'v', ratio: 0.55,
            a: { kind: 'pane', id: 'p5', widget: 'shepherd-assessment-card', config: {} },
            b: { kind: 'pane', id: 'p6', widget: 'inbox', config: {} },
          },
        },
      },
    };
  }

  // --- API ---
  const API = '/api/v1';
  async function refresh() {
    try {
      const [s, t, m, ib, ev] = await Promise.all([
        fetch(`${API}/sessions`).then(r => r.json()),
        fetch(`${API}/teams`).then(r => r.json()),
        fetch(`${API}/memberships`).then(r => r.json()),
        fetch(`${API}/inbox`).then(r => r.json()),
        fetch(`${API}/events?limit=80`).then(r => r.json()),
      ]);
      sessions = s.sessions || [];
      teams = t.teams || [];
      memberships = m.memberships || [];
      inbox = ib.inbox || [];
      events = ev.events || [];
      if (selectedAgent && !sessions.find(s => s.name === selectedAgent)) {
        selectedAgent = null;
      }
      // Auto-pick first non-shepherd worker when nothing is selected — so
      // AgentDetails / terminal / prompt / skills widgets render real
      // content without requiring an extra click after opening the dashboard.
      if (!selectedAgent && sessions.length) {
        const w = sessions.find(s => !s.exited && s.role !== 'shepherd')
              || sessions.find(s => !s.exited);
        if (w) selectedAgent = w.name;
      }
    } catch {}
  }

  let evStream = null;
  function startEventStream() {
    if (evStream) return;
    const tok = getStoredToken();
    const q = tok ? ('?token=' + encodeURIComponent(tok)) : '';
    evStream = new EventSource(`${API}/events/stream${q}`);
    evStream.onmessage = (ev) => {
      try {
        const e = JSON.parse(ev.data);
        events = [...events, e].slice(-200);
      } catch {}
    };
    evStream.onerror = () => { evStream?.close(); evStream = null; setTimeout(startEventStream, 3000); };
  }

  // --- workspace save/load ---
  let activeWorkspace = $state('');   // name of the currently-active named workspace
  let projectCwd = $state('');        // CWD associated with active workspace

  // --- pane focus (Ctrl+Arrow navigation) ---
  let focusedPaneID = $state('');
  function flattenLeafIDs(node, out = []) {
    if (!node) return out;
    if (node.kind === 'pane') { out.push(node.id); return out; }
    flattenLeafIDs(node.a, out);
    flattenLeafIDs(node.b, out);
    return out;
  }
  function firstLeafID(node) {
    const list = flattenLeafIDs(node);
    return list[0] || '';
  }
  function movePaneFocus(direction) {
    const list = flattenLeafIDs(layout);
    if (list.length === 0) return;
    if (!focusedPaneID || !list.includes(focusedPaneID)) {
      focusedPaneID = list[0];
      return;
    }
    // For now Left/Up = prev, Right/Down = next (flatten order).
    // Spatial 2D nav across the split tree is a future enhancement.
    const i = list.indexOf(focusedPaneID);
    const delta = (direction === 'left' || direction === 'up') ? -1 : +1;
    const j = (i + delta + list.length) % list.length;
    focusedPaneID = list[j];
    // Push DOM focus into the new pane so xterm receives keys.
    queueMicrotask(() => {
      const el = document.querySelector(`[data-pane-id="${focusedPaneID}"]`);
      if (el) {
        const target = el.querySelector('.xterm-helper-textarea, .xterm textarea, textarea, input, button') || el;
        try { target.focus?.(); } catch {}
      }
    });
  }

  // --- key hijack mode (capture vs passthrough) ---
  let captureMode = $state(true);     // default ON per operator decision
  // #397 P0 — visible state + Ctrl+B command palette + ? help overlay.
  // Architect: operator couldn't discover that Ctrl+Shift+Esc was the
  // capture-mode toggle. Adding three discovery aids:
  //   1. Topbar indicator showing capture state (rendered below)
  //   2. Ctrl+B opens a command palette listing every shortcut + action
  //   3. ? key opens a cheatsheet overlay (passthrough — works without
  //      capture mode)
  let showCmdPalette = $state(false);
  let showShortcutHelp = $state(false);
  let cmdPaletteFilter = $state('');
  $effect(() => {
    function onKey(e) {
      // Release toggle: Ctrl+Shift+Esc — always works regardless of mode.
      if (e.ctrlKey && e.shiftKey && (e.key === 'Escape' || e.code === 'Escape')) {
        e.preventDefault();
        captureMode = !captureMode;
        return;
      }
      // #397 P0 — Ctrl+B (chepherd command prefix) ALWAYS captures,
      // even when captureMode is off. The prefix is the operator's
      // "I want to talk to chepherd" anchor — must be reliable.
      // Mirrors tmux's prefix-key discoverability pattern.
      if ((e.ctrlKey || e.metaKey) && (e.key === 'b' || e.key === 'B') && !e.shiftKey && !e.altKey) {
        e.preventDefault();
        e.stopPropagation();
        showCmdPalette = !showCmdPalette;
        cmdPaletteFilter = '';
        showShortcutHelp = false;
        return;
      }
      // #397 P0 — ? key (without modifiers, when not in a text input)
      // opens the keyboard cheatsheet overlay. Passthrough behavior
      // so it works even when captureMode is off — discovery aid.
      if (e.key === '?' && !e.ctrlKey && !e.metaKey && !e.altKey) {
        const t = e.target;
        const isText = t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable ||
                            (t.classList && (t.classList.contains('xterm-helper-textarea') || t.closest?.('.xterm'))));
        if (!isText) {
          e.preventDefault();
          showShortcutHelp = !showShortcutHelp;
          showCmdPalette = false;
          return;
        }
      }
      // Escape closes the overlays.
      if (e.key === 'Escape' && !e.ctrlKey && !e.shiftKey && !e.altKey) {
        if (showCmdPalette || showShortcutHelp) {
          e.preventDefault();
          showCmdPalette = false;
          showShortcutHelp = false;
          return;
        }
      }
      if (!captureMode) return;
      // Ctrl+Alt+* is the universal-fallback flavor that browsers don't
      // intercept; the plain Ctrl+* variants work in PWA/Electron mode.
      const isCmd = (e.ctrlKey || e.metaKey);
      if (!isCmd) return;
      // Honest binding list (operator 2026-05-29: 'you couldnt lock it'):
      //   Tab cycling   → Ctrl+Alt+Tab          (+Shift for prev)
      //   New tab       → Ctrl+Alt+T
      //   Pane focus    → Ctrl+Arrow            (already working)
      //   Backtick alt  → Ctrl+`  cycles tabs   (VS Code / kitty style)
      //
      // Plain Ctrl+T and Ctrl+Tab are intentionally NOT bound. Chrome
      // / Firefox / Safari capture them at the OS layer and never
      // deliver the keydown to the renderer — JS literally cannot
      // intercept them in a regular browser tab. They work only when
      // chepherd is installed as a PWA (Chrome menu → "Install
      // chepherd…") or wrapped in Electron. Telling the operator they
      // "should" work in a browser tab would be a lie.
      if (e.altKey && e.key === 'Tab') {
        e.preventDefault();
        if (focusedPaneID) {
          window.dispatchEvent(new CustomEvent('chepherd-pane-cycle-tab', {
            detail: { paneID: focusedPaneID, direction: e.shiftKey ? -1 : +1 },
          }));
        }
        return;
      }
      if (e.altKey && (e.key === 't' || e.key === 'T')) {
        e.preventDefault();
        if (focusedPaneID) {
          window.dispatchEvent(new CustomEvent('chepherd-pane-new-tab', {
            detail: { paneID: focusedPaneID },
          }));
        }
        return;
      }
      // Ctrl+` (backtick) — bonus tab cycle that works in plain browser
      // (no Alt needed, no OS conflict). VS Code uses Ctrl+` for
      // terminal toggle; we repurpose it here.
      if (e.key === '`' && !e.altKey) {
        e.preventDefault();
        if (focusedPaneID) {
          window.dispatchEvent(new CustomEvent('chepherd-pane-cycle-tab', {
            detail: { paneID: focusedPaneID, direction: e.shiftKey ? -1 : +1 },
          }));
        }
        return;
      }
      // Pane navigation — Ctrl+Arrow / Ctrl+Alt+Arrow.
      if (e.key === 'ArrowLeft' || e.key === 'ArrowRight' || e.key === 'ArrowUp' || e.key === 'ArrowDown') {
        e.preventDefault();
        movePaneFocus(e.key.replace('Arrow','').toLowerCase());
        return;
      }
    }
    window.addEventListener('keydown', onKey, true);  // capture phase — beats xterm
    return () => window.removeEventListener('keydown', onKey, true);
  });


  async function saveLayout(name = 'current') {
    const body = name === 'current'
      ? layout  // 'current' stays as bare layout for backwards compat
      : { layout, cwd: projectCwd };
    await fetch(`${API}/workspaces/${name}`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
  }
  async function loadLayout(name) {
    try {
      const r = await fetch(`${API}/workspaces/${name}`);
      if (!r.ok) return;
      const d = await r.json();
      // New envelope format: { layout, cwd }. Legacy: bare layout tree.
      if (d.layout) { layout = d.layout; if (d.cwd) projectCwd = d.cwd; }
      else layout = d;
    } catch {}
  }

  // --- pane operations ---
  function selectAgent(name) {
    selectedAgent = name;
  }

  function changeWidget(paneId, newWidget) {
    layout = setWidget(layout, paneId, newWidget);
    saveLayout();
  }
  function setWidget(node, id, widget) {
    if (node.kind === 'pane') {
      if (node.id === id) return { ...node, widget };
      return node;
    }
    return { ...node, a: setWidget(node.a, id, widget), b: setWidget(node.b, id, widget) };
  }
  function splitPane(paneId, direction) {
    layout = doSplit(layout, paneId, direction);
    saveLayout();
  }
  function doSplit(node, id, direction) {
    if (node.kind === 'pane' && node.id === id) {
      const newId = 'p' + Date.now();
      const newPane = { kind: 'pane', id: newId, widget: 'agent-details', config: {} };
      return { kind: direction, ratio: 0.5, a: node, b: newPane };
    }
    if (node.kind !== 'pane') {
      return { ...node, a: doSplit(node.a, id, direction), b: doSplit(node.b, id, direction) };
    }
    return node;
  }
  function removePane(paneId) {
    layout = doRemove(layout, paneId) ?? layout;
    saveLayout();
  }
  function doRemove(node, id) {
    if (node.kind === 'pane') return node.id === id ? null : node;
    const ra = doRemove(node.a, id);
    const rb = doRemove(node.b, id);
    if (ra === null) return rb;
    if (rb === null) return ra;
    return { ...node, a: ra, b: rb };
  }

  // --- template apply ---
  async function applyTemplate(name, team, cwd) {
    const r = await fetch(`${API}/templates/${name}/apply`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ team, cwd }),
    });
    showTemplates = false;
    await refresh();
    return r.json();
  }

  // --- workspace templates ---
  function applyWorkspaceTemplate(name) {
    switch (name) {
      case 'focus': layout = defaultFocusLayout(); break;
      case 'council': layout = councilLayout(); break;
      case 'board': layout = boardLayout(); break;
      case 'multi-team': layout = multiTeamLayout(); break;
    }
    saveLayout();
  }
  function onViewChange(ev) {
    const v = ev.target.value;
    if (!v) return;
    if (v.startsWith('preset:')) applyWorkspaceTemplate(v.slice(7));
    else if (v.startsWith('saved:')) loadSaved(v.slice(6));
    ev.target.value = '';
  }
  function councilLayout() {
    return {
      kind: 'h', ratio: 0.18,
      a: { kind: 'pane', id: 'p1', widget: 'session-list', config: {} },
      b: {
        kind: 'v', ratio: 0.78,
        a: { kind: 'pane', id: 'p2', widget: 'session-board', config: {} },
        b: { kind: 'pane', id: 'p3', widget: 'events', config: {} },
      },
    };
  }
  function boardLayout() {
    return {
      kind: 'v', ratio: 0.85,
      a: { kind: 'pane', id: 'p1', widget: 'session-board', config: {} },
      b: { kind: 'pane', id: 'p2', widget: 'events', config: {} },
    };
  }
  function multiTeamLayout() {
    return {
      kind: 'h', ratio: 0.22,
      a: { kind: 'pane', id: 'p1', widget: 'session-list', config: { groupBy: 'team' } },
      b: { kind: 'pane', id: 'p2', widget: 'terminal', config: {} },
    };
  }

  // --- workspace font-size (granular, applies to ALL widgets uniformly) ---
  // Uses a CSS variable --ws-font so every widget inherits.
  let fontSize = $state(14);
  function applyFontSize(n) {
    fontSize = Math.max(9, Math.min(22, n));
    document.documentElement.style.setProperty('--ws-font', fontSize + 'px');
    // Also drive the root rem base so all rem-based UI elements scale with A+/A-.
    document.documentElement.style.fontSize = fontSize + 'px';
    try { localStorage.setItem('chepherd-font', String(fontSize)); } catch {}
  }
  // Default font on first load: 14 (overridable via localStorage from prior session)

  // --- view picker (custom dropdown replacing native <select>) ---
  let showViewMenu = $state(false);
  function pickView(val) { showViewMenu = false; onViewChange({ target: { value: val } }); }

  // --- save-as named layout ---
  let showSaveAs = $state(false);
  let saveAsName = $state('');
  let savedLayouts = $state([]);
  async function listSavedLayouts() {
    try { const r = await fetch(`${API}/workspaces`); const d = await r.json(); savedLayouts = d.workspaces || []; } catch {}
  }
  let saveAsCwd = $state('');
  async function saveAs() {
    if (!saveAsName.trim()) return;
    if (saveAsCwd.trim()) projectCwd = saveAsCwd.trim();
    await saveLayout(saveAsName.trim());
    activeWorkspace = saveAsName.trim();
    await listSavedLayouts();
    showSaveAs = false;
    saveAsName = '';
    saveAsCwd = '';
  }
  async function loadSaved(n) {
    await loadLayout(n);
    activeWorkspace = n;
  }

  // --- agent action menu (stop/pause/restart) ---
  let showAgentMenu = $state(false);
  let showHandoffPicker = $state(false);
  let handoffTarget = $state('');
  async function agentAction(act) {
    if (!selectedAgent) return;
    let url, method, body;
    switch (act) {
      case 'pause':   url = `${API}/sessions/${selectedAgent}/pause`; method = 'POST'; body = JSON.stringify({ paused: true });  break;
      case 'unpause': url = `${API}/sessions/${selectedAgent}/pause`; method = 'POST'; body = JSON.stringify({ paused: false }); break;
      case 'restart': url = `${API}/sessions/${selectedAgent}/restart`; method = 'POST'; body = null; break;
      case 'stop':    url = `${API}/sessions/${selectedAgent}`; method = 'DELETE'; body = null; break;
    }
    try {
      const r = await fetch(url, { method, headers: body ? { 'Content-Type': 'application/json' } : {}, body });
      if (!r.ok) { const e = await r.json().catch(()=>({})); alert(e.error || `HTTP ${r.status}`); }
    } catch (e) { alert(String(e)); }
    await refresh();
    showAgentMenu = false;
  }

  async function doHandoff() {
    if (!selectedAgent || !handoffTarget) return;
    try {
      const r = await fetch(`${API}/sessions/${selectedAgent}/handoff`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ target: handoffTarget }),
      });
      if (!r.ok) { const e = await r.json().catch(()=>({})); alert(e.error || `HTTP ${r.status}`); return; }
    } catch (e) { alert(String(e)); return; }
    showHandoffPicker = false;
    showAgentMenu = false;
    handoffTarget = '';
    await refresh();
  }

  // --- auth (#157) — wrap window.fetch so every /api/* request gets the
  // bearer token. Token is stored in localStorage; if missing, prompt for it.
  function getStoredToken() {
    if (typeof localStorage === 'undefined') return '';
    try { return localStorage.getItem('chepherd-token') || ''; } catch { return ''; }
  }
  // Plain $state(false) — Astro SSR runs this on the server (no localStorage),
  // so initialising with an expression that touches `window` would lock the
  // value to false during hydration. Instead we flip it on mount.
  let needLogin = $state(false);
  let loginTokenInput = $state('');
  let loginError = $state('');
  // installFetchAuth moved to module top-level (#157 timing fix).
  async function saveLoginToken() {
    if (!loginTokenInput.trim()) { loginError = 'paste the bootstrap token'; return; }
    try { localStorage.setItem('chepherd-token', loginTokenInput.trim()); } catch {}
    // Test the token via /healthz (public) + then /api/v1/sessions (gated).
    try {
      const r = await fetch('/api/v1/sessions');
      if (r.status === 401) { loginError = 'token rejected'; return; }
    } catch (e) { loginError = String(e); return; }
    needLogin = false; loginError = ''; loginTokenInput = '';
    refresh();
  }

  // --- mount ---
  onMount(() => {
    try { theme = localStorage.getItem('chepherd-theme') || 'dark'; document.documentElement.dataset.theme = theme; } catch {}
    try { const f = +(localStorage.getItem('chepherd-font') || 14); applyFontSize(f); } catch { applyFontSize(14); }
    if (!getStoredToken()) { needLogin = true; }
    window.addEventListener('chepherd-401', () => { needLogin = true; });
    refresh();
    listSavedLayouts();
    const intv = setInterval(refresh, 2500);
    startEventStream();
    loadLayout('current');
    // Initialise pane focus to the first leaf once a layout exists.
    setTimeout(() => { if (!focusedPaneID) focusedPaneID = firstLeafID(layout); }, 250);
    const onTeamSettings = (ev) => { showTeamSettings = ev.detail; };
    const onAgentSettings = (ev) => { selectedAgent = ev.detail.agentName; showAgentSettings = true; };
    const onAddMember = (ev) => { showWizard = true; /* TeamSettings emits this; wizard handles spawn-with-team-prefilled */ };
    window.addEventListener('chepherd-open-team-settings', onTeamSettings);
    window.addEventListener('chepherd-open-agent-settings', onAgentSettings);
    window.addEventListener('chepherd-add-member', onAddMember);
    return () => {
      clearInterval(intv); evStream?.close();
      window.removeEventListener('chepherd-open-team-settings', onTeamSettings);
      window.removeEventListener('chepherd-open-agent-settings', onAgentSettings);
      window.removeEventListener('chepherd-add-member', onAddMember);
    };
  });

  function toggleTheme() {
    theme = theme === 'dark' ? 'light' : 'dark';
    document.documentElement.dataset.theme = theme;
    try { localStorage.setItem('chepherd-theme', theme); } catch {}
  }

  // #239 — logout dispatch. AuthGate (../v09/AuthGate.svelte) listens
  // for this event + clears the stored token + re-renders the login
  // screen. Keeping the dispatch here so legacy callers (v0.8 / v0.9.0
  // / v0.9.1 routes wired directly to Workspace pre-AuthGate) still
  // get a sane fallback: if AuthGate isn't mounted, the event is
  // harmless + Workspace's needLogin in-place modal still fires on
  // the next 401.
  function dispatchLogout() {
    try { window.dispatchEvent(new CustomEvent('chepherd-logout')); } catch {}
  }
</script>

<div class="workspace">
  <header class="topbar">
    <a href="/" class="brand"><span class="brand-mark">░</span>chepherd<span class="ver">{version}</span></a>
    <div class="stats">
      <!-- #148 — pluralize correctly. "1 agent" not "1 agents". -->
      {sessions.length} {sessions.length === 1 ? 'agent' : 'agents'} · {teams.length} {teams.length === 1 ? 'team' : 'teams'} · {memberships.length} {memberships.length === 1 ? 'membership' : 'memberships'}
    </div>
    {#if activeWorkspace}<span class="workspace-badge" title="active project workspace">{activeWorkspace}</span>{/if}
    <div class="view-menu-wrap" use:clickOutside={() => (showViewMenu = false)}>
      <button class="view-btn" on:click={() => (showViewMenu = !showViewMenu)} title="Switch layout">
        <svg width="14" height="14" viewBox="0 0 14 14" fill="none" xmlns="http://www.w3.org/2000/svg">
          <rect x="1" y="1" width="5" height="5" rx="1" stroke="currentColor" stroke-width="1.4"/>
          <rect x="8" y="1" width="5" height="5" rx="1" stroke="currentColor" stroke-width="1.4"/>
          <rect x="1" y="8" width="5" height="5" rx="1" stroke="currentColor" stroke-width="1.4"/>
          <rect x="8" y="8" width="5" height="5" rx="1" stroke="currentColor" stroke-width="1.4"/>
        </svg>
        View
        <svg class="caret" width="10" height="6" viewBox="0 0 10 6" fill="currentColor"><path d="M1 1l4 4 4-4"/></svg>
      </button>
      {#if showViewMenu}
        <div class="view-dropdown" role="menu">
          <div class="vd-section">Layout</div>
          <button role="menuitem" on:click={() => pickView('preset:focus')}>Focus</button>
          <button role="menuitem" on:click={() => pickView('preset:council')}>Council</button>
          <button role="menuitem" on:click={() => pickView('preset:board')}>Board</button>
          <button role="menuitem" on:click={() => pickView('preset:multi-team')}>Multi</button>
          {#if savedLayouts.filter(n => n !== 'current').length}
            <div class="vd-divider"></div>
            <div class="vd-section">Saved</div>
            {#each savedLayouts.filter(n => n !== 'current') as n}
              <button role="menuitem" on:click={() => pickView('saved:' + n)}>{n}</button>
            {/each}
          {/if}
        </div>
      {/if}
    </div>
    <div class="font-knob" title="font size (applies to all widgets)">
      <button class="icon-btn small" on:click={() => applyFontSize(fontSize - 1)} aria-label="smaller">A-</button>
      <span class="font-num">{fontSize}px</span>
      <button class="icon-btn small" on:click={() => applyFontSize(fontSize + 1)} aria-label="larger">A+</button>
    </div>
    {#if selectedAgent}
      <div class="agent-menu">
        <button class="secondary" on:click={() => (showAgentMenu = !showAgentMenu)} title="agent actions">{selectedAgent} ▾</button>
        {#if showAgentMenu}
          <div class="dropdown">
            <button on:click={() => { showAgentSettings = true; showAgentMenu = false; }}>⚙ Settings…</button>
            <button on:click={() => agentAction('pause')}>⏸ Pause</button>
            <button on:click={() => agentAction('unpause')}>▶ Resume</button>
            <button on:click={() => agentAction('restart')}>↻ Restart</button>
            <button on:click={() => (showHandoffPicker = !showHandoffPicker)}>⇄ Hand off to…</button>
            {#if showHandoffPicker}
              <div class="handoff-row">
                <select bind:value={handoffTarget}>
                  <option value="">pick target…</option>
                  {#each sessions.filter(s => s.name !== selectedAgent && !s.exited) as s}
                    <option value={s.name}>{s.name}</option>
                  {/each}
                </select>
                <button class="primary-sm" on:click={doHandoff} disabled={!handoffTarget}>Go</button>
              </div>
            {/if}
            <button class="danger" on:click={() => agentAction('stop')}>■ Stop</button>
          </div>
        {/if}
      </div>
    {/if}
    <!-- #397 P0 — keyboard-shortcuts state indicator + command-palette
         opener. Hover for full tooltip; click to open palette. -->
    <button class="icon-btn capture-state"
            class:on={captureMode} class:off={!captureMode}
            on:click={() => { showCmdPalette = !showCmdPalette; cmdPaletteFilter = ''; }}
            title={captureMode
              ? 'Shortcuts ON · Ctrl+Shift+Esc to disable · Ctrl+B for command palette · ? for cheatsheet'
              : 'Shortcuts OFF · Ctrl+Shift+Esc to enable · Ctrl+B still opens palette · ? still opens cheatsheet'}
            aria-label="keyboard shortcuts state">
      ⌨{captureMode ? '' : '✕'}
    </button>
    <button class="icon-btn" on:click={() => { showShortcutHelp = !showShortcutHelp; }} title="Keyboard shortcuts cheatsheet (press ?)" aria-label="Keyboard cheatsheet">?</button>
    <button class="icon-btn" on:click={toggleTheme} title="Toggle theme">{theme === 'dark' ? '☀' : '☾'}</button>
    <button class="icon-btn" on:click={dispatchLogout} title="Sign out — clear stored token + return to login screen" aria-label="Sign out">⎋</button>
    <button class="save-layout-btn" on:click={() => (showSaveAs = true)} title="Save current layout as a named view">
      <svg width="16" height="16" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg">
        <path d="M13 14H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1h8l2 2v9a1 1 0 0 1-1 1z" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/>
        <rect x="5" y="9" width="6" height="5" rx="0.5" stroke="currentColor" stroke-width="1.2"/>
        <rect x="5" y="2" width="4" height="3" rx="0.5" stroke="currentColor" stroke-width="1.2"/>
      </svg>
    </button>
    <button class="primary spawn-btn" on:click={() => (showWizard = true)} title="Spawn a single agent or apply a team template">+ new</button>
  </header>

  <div class="canvas">
    <Pane node={layout} {sessions} {teams} {memberships} {inbox} {events} {selectedAgent} {selectAgent} {changeWidget} {splitPane} {removePane} {refresh} {focusedPaneID} setFocusedPane={(id) => focusedPaneID = id} saveLayout={() => saveLayout()} />
  </div>
</div>

{#if needLogin}
  <!-- #157 — bootstrap-token login modal. Operator pastes the JWT
       printed at chepherd startup; stored in localStorage so subsequent
       page loads skip this screen. -->
  <div class="backdrop">
    <div class="modal-login">
      <h2>🔑 chepherd login</h2>
      <p class="prose">Paste the bootstrap token chepherd printed at startup.</p>
      <p class="prose tiny">Find it with: <code>podman exec chepherd cat /home/chepherd/.local/state/chepherd/auth.printed</code></p>
      <textarea bind:value={loginTokenInput} placeholder="eyJhbGc…" rows="4"></textarea>
      {#if loginError}<div class="error">{loginError}</div>{/if}
      <button class="primary" on:click={saveLoginToken}>Sign in</button>
    </div>
  </div>
{/if}
{#if showWizard}
  <div class="wizard-overlay" role="dialog">
    {#if typeof window !== 'undefined' && new URLSearchParams(window.location.search).get('wizard') === 'v08'}
      <SpawnWizard onClose={() => (showWizard = false)} onLaunched={refresh} defaultCwd={projectCwd} />
    {:else}
      <SpawnWizardV9 onclose={() => { showWizard = false; refresh(); }} />
    {/if}
  </div>
{/if}
{#if showAgentSettings && selectedAgent}
  {@const ag = sessions.find(s => s.name === selectedAgent)}
  {#if ag}<AgentSettings agent={ag} {teams} onClose={() => (showAgentSettings = false)} />{/if}
{/if}
{#if showTeamSettings}
  <TeamSettings team={showTeamSettings.team} members={showTeamSettings.members} onClose={() => (showTeamSettings = null)} onChanged={refresh} />
{/if}
{#if showSaveAs}
  <div class="backdrop" on:click={() => (showSaveAs = false)}>
    <div class="modal-saveas" on:click|stopPropagation>
      <h3>Save project workspace…</h3>
      <label>Workspace name<input bind:value={saveAsName} placeholder="my-project" autofocus /></label>
      <label>Project CWD (optional — used as default cwd for new spawns when this workspace is active)
        <input bind:value={saveAsCwd} placeholder="/home/openova/repos/my-project" />
      </label>
      <p class="hint">Workspaces save your pane layout + a project root. Switch via the View dropdown.</p>
      <footer>
        <button class="secondary" on:click={() => (showSaveAs = false)}>Cancel</button>
        <button class="primary" on:click={saveAs} disabled={!saveAsName.trim()}>Save</button>
      </footer>
    </div>
  </div>
{/if}

<!-- #397 P0 — command palette (Ctrl+B). Discoverable list of every
     chepherd action. Filter via the input. Click an item to run. -->
{#if showCmdPalette}
  <div class="overlay" on:click={() => { showCmdPalette = false; }}>
    <div class="palette" on:click|stopPropagation>
      <input class="palette-input" placeholder="Type to filter…  (Ctrl+B closes · Esc closes)"
             bind:value={cmdPaletteFilter}
             autofocus
             on:keydown={(e) => { if (e.key === 'Enter') { /* future: run first match */ } }} />
      <div class="palette-list">
        {#each [
          { id: 'cmd-new-tab', label: 'New tab in focused pane', shortcut: 'Ctrl+Alt+T', action: () => focusedPaneID && window.dispatchEvent(new CustomEvent('chepherd-pane-new-tab', { detail: { paneID: focusedPaneID } })) },
          { id: 'cmd-cycle-tab', label: 'Cycle to next tab', shortcut: 'Ctrl+Alt+Tab · Ctrl+`', action: () => focusedPaneID && window.dispatchEvent(new CustomEvent('chepherd-pane-cycle-tab', { detail: { paneID: focusedPaneID, direction: +1 } })) },
          { id: 'cmd-cycle-tab-prev', label: 'Cycle to previous tab', shortcut: 'Ctrl+Shift+Alt+Tab', action: () => focusedPaneID && window.dispatchEvent(new CustomEvent('chepherd-pane-cycle-tab', { detail: { paneID: focusedPaneID, direction: -1 } })) },
          { id: 'cmd-pane-left', label: 'Focus pane to the left', shortcut: 'Ctrl+←', action: () => movePaneFocus('left') },
          { id: 'cmd-pane-right', label: 'Focus pane to the right', shortcut: 'Ctrl+→', action: () => movePaneFocus('right') },
          { id: 'cmd-pane-up', label: 'Focus pane above', shortcut: 'Ctrl+↑', action: () => movePaneFocus('up') },
          { id: 'cmd-pane-down', label: 'Focus pane below', shortcut: 'Ctrl+↓', action: () => movePaneFocus('down') },
          { id: 'cmd-help', label: 'Show keyboard cheatsheet', shortcut: '?', action: () => { showShortcutHelp = true; showCmdPalette = false; } },
          { id: 'cmd-toggle-capture', label: captureMode ? 'Disable shortcuts (passthrough mode)' : 'Enable shortcuts (capture mode)', shortcut: 'Ctrl+Shift+Esc', action: () => { captureMode = !captureMode; } },
          { id: 'cmd-toggle-theme', label: 'Toggle theme', shortcut: '', action: () => toggleTheme() },
          { id: 'cmd-logout', label: 'Sign out', shortcut: '', action: () => dispatchLogout() },
        ].filter(c => !cmdPaletteFilter || c.label.toLowerCase().includes(cmdPaletteFilter.toLowerCase())) as cmd (cmd.id)}
          <button class="palette-item" on:click={() => { cmd.action(); if (cmd.id !== 'cmd-help') showCmdPalette = false; }}>
            <span class="pl-label">{cmd.label}</span>
            {#if cmd.shortcut}<span class="pl-key">{cmd.shortcut}</span>{/if}
          </button>
        {/each}
      </div>
      <div class="palette-footer">Esc · Ctrl+B to close</div>
    </div>
  </div>
{/if}

<!-- #397 P0 — keyboard cheatsheet overlay (?). Read-only reference,
     no actions. Plain Ctrl+T / Ctrl+Tab caveat captured per existing
     onKey comment. -->
{#if showShortcutHelp}
  <div class="overlay" on:click={() => { showShortcutHelp = false; }}>
    <div class="cheatsheet" on:click|stopPropagation>
      <header class="cs-head">
        <h2>Keyboard shortcuts</h2>
        <button class="icon-btn" on:click={() => { showShortcutHelp = false; }} title="Close (Esc)">✕</button>
      </header>
      <table class="cs-table">
        <tbody>
          <tr><td class="cs-k">Ctrl+B</td><td>Open command palette</td></tr>
          <tr><td class="cs-k">?</td><td>Show / hide this cheatsheet</td></tr>
          <tr><td class="cs-k">Ctrl+Shift+Esc</td><td>Toggle shortcut capture mode (on/off)</td></tr>
          <tr><td class="cs-section" colspan="2">Tabs</td></tr>
          <tr><td class="cs-k">Ctrl+Alt+Tab</td><td>Cycle to next tab in focused pane</td></tr>
          <tr><td class="cs-k">Ctrl+Shift+Alt+Tab</td><td>Cycle to previous tab</td></tr>
          <tr><td class="cs-k">Ctrl+Alt+T</td><td>New tab in focused pane</td></tr>
          <tr><td class="cs-k">Ctrl+`</td><td>Cycle tabs (no Alt — plain browser friendly)</td></tr>
          <tr><td class="cs-section" colspan="2">Pane navigation</td></tr>
          <tr><td class="cs-k">Ctrl+←/→/↑/↓</td><td>Focus pane in that direction</td></tr>
          <tr><td class="cs-section" colspan="2">Notes</td></tr>
        </tbody>
      </table>
      <p class="cs-note">
        Plain <kbd>Ctrl+T</kbd> and <kbd>Ctrl+Tab</kbd> are intentionally not bound —
        browsers capture them at the OS layer. Install chepherd as a PWA
        (Chrome menu → "Install chepherd…") for those to work too.
      </p>
      <p class="cs-note">
        Capture is <strong>{captureMode ? 'ON' : 'OFF'}</strong>. The ⌨ topbar icon
        shows live state. Toggle via <kbd>Ctrl+Shift+Esc</kbd>.
      </p>
      <footer class="cs-foot"><button class="primary" on:click={() => { showShortcutHelp = false; }}>Got it (Esc)</button></footer>
    </div>
  </div>
{/if}

<style>
  .wizard-overlay {
    position: fixed; inset: 0;
    background: rgba(0,0,0,0.55);
    display: flex; align-items: center; justify-content: center;
    z-index: 1000;
  }
  :global(html[data-theme="dark"]) {
    --bg: #0a0a0a; --bg-elev: #111; --bg-input: #0a0a0a;
    --border: #1e1e1e; --border-strong: #2a2a2a;
    --fg: #f5f5f5; --fg-muted: #aaa; --fg-faint: #666;
    --accent: #0072F5; --accent-2: #87ceeb; --danger: #ff6b6b;
    --select-bg: #0d1f3c; --select-border: #0072F5;
    --scrollbar-track: transparent;
    --scrollbar-thumb: #2a2a2a;
    --scrollbar-thumb-hover: #3a3a3a;
  }
  :global(html[data-theme="light"]) {
    --bg: #fafafa; --bg-elev: #ffffff; --bg-input: #ffffff;
    --border: #e5e7eb; --border-strong: #cbd5e1;
    --fg: #1a1a1a; --fg-muted: #555; --fg-faint: #888;
    --accent: #0057c7; --accent-2: #2563eb; --danger: #c92020;
    --select-bg: #e0f2fe; --select-border: #0057c7;
    --scrollbar-track: transparent;
    --scrollbar-thumb: #cbd5e1;
    --scrollbar-thumb-hover: #94a3b8;
  }
  /* Match v0.5 scrollbars — thin track, rounded thumb, hover→darker, active→accent. */
  :global(*) { scrollbar-width: thin; scrollbar-color: var(--scrollbar-thumb) var(--scrollbar-track); }
  :global(*::-webkit-scrollbar) { width: 12px; height: 12px; }
  :global(*::-webkit-scrollbar-track) { background: var(--scrollbar-track); }
  :global(*::-webkit-scrollbar-thumb) { background: var(--scrollbar-thumb); border-radius: 10px; border: 2px solid var(--bg); min-height: 40px; }
  :global(*::-webkit-scrollbar-thumb:hover) { background: var(--scrollbar-thumb-hover); }
  :global(*::-webkit-scrollbar-thumb:active) { background: var(--accent); }
  :global(*::-webkit-scrollbar-corner) { background: transparent; }
  :global(.xterm-viewport::-webkit-scrollbar) { width: 12px; }
  :global(.xterm-viewport::-webkit-scrollbar-track) { background: var(--scrollbar-track); }
  :global(.xterm-viewport::-webkit-scrollbar-thumb) { background: var(--scrollbar-thumb); border-radius: 10px; border: 2px solid var(--bg); min-height: 40px; }
  :global(.xterm-viewport::-webkit-scrollbar-thumb:hover) { background: var(--scrollbar-thumb-hover); }
  /* Single source of truth for type sizing (#2 audit + unify):
     html's font-size IS --ws-font, so every `rem` value in every
     component scales automatically when the operator hits A+/A-.
     No per-component overrides needed. */
  :global(html) { --ws-font: 14px; font-size: var(--ws-font); }
  :global(html), :global(body) { background: var(--bg); color: var(--fg); margin: 0; padding: 0; height: 100vh; overflow: hidden; font-family: ui-sans-serif, system-ui, sans-serif; }
  /* Force all form controls to inherit the base font — browsers default
     buttons/inputs/selects to a system UI font that can differ on some platforms. */
  :global(button), :global(input), :global(select), :global(textarea) { font-family: inherit; }
  /* Single source of truth: every body-text descendant of a .pane-body
     uses var(--ws-font). Chrome elements (chips, badges, small notes)
     are em-scaled relative to ws-font so they always feel proportional
     to the body. The font-knob in the top bar is the only place that
     changes type sizing. */
  :global(.pane-body) { font-size: var(--ws-font); }
  :global(.pane-body) :global(p), :global(.pane-body) :global(li),
  :global(.pane-body) :global(td), :global(.pane-body) :global(th),
  :global(.pane-body) :global(dt), :global(.pane-body) :global(dd),
  :global(.pane-body) :global(pre), :global(.pane-body) :global(code),
  :global(.pane-body) :global(input), :global(.pane-body) :global(select),
  :global(.pane-body) :global(textarea), :global(.pane-body) :global(button),
  :global(.pane-body) :global(label), :global(.pane-body) :global(span):not(.chip):not(.badge),
  :global(.pane-body) :global(.body), :global(.pane-body) :global(.area),
  :global(.pane-body) :global(.kv), :global(.pane-body) :global(.list),
  :global(.pane-body) :global(.events) { font-size: var(--ws-font) !important; }
  :global(.pane-body) :global(h1), :global(.pane-body) :global(h2),
  :global(.pane-body) :global(h3) { font-size: calc(var(--ws-font) * 1.18) !important; }
  :global(.pane-body) :global(h4) { font-size: calc(var(--ws-font) * 1.05) !important; }
  :global(.pane-body) :global(h5), :global(.pane-body) :global(h6) { font-size: var(--ws-font) !important; }
  :global(.pane-body) :global(small), :global(.pane-body) :global(.hint),
  :global(.pane-body) :global(.empty), :global(.pane-body) :global(.tiny),
  :global(.pane-body) :global(.muted) { font-size: calc(var(--ws-font) * 0.88) !important; }
  :global(.pane-body) :global(.chip), :global(.pane-body) :global(.badge) {
    font-size: calc(var(--ws-font) * 0.82) !important;
  }
  .workspace { display: flex; flex-direction: column; height: 100vh; background: var(--bg); color: var(--fg); }
  .topbar { display: flex; align-items: center; gap: 0.9rem; padding: 0.72rem 1.2rem; background: var(--bg-elev); border-bottom: 1px solid var(--border); }
  .topbar .brand { color: var(--fg); font-weight: 700; text-decoration: none; font-size: 1.1rem; letter-spacing: -0.02em; font-family: ui-monospace, monospace; display: flex; align-items: baseline; gap: 0.2rem; }
  .topbar .brand .brand-mark { color: var(--accent); font-size: 1.2em; }
  .topbar .brand .ver { font-size: 0.65rem; color: var(--fg-faint); margin-left: 0.35rem; font-weight: 400; align-self: center; }
  .topbar .stats { flex: 1; color: var(--fg-muted); font-size: 0.78rem; white-space: nowrap; }
  .workspace-badge { font-size: 0.72rem; color: var(--accent-2); background: color-mix(in srgb, var(--accent-2) 10%, transparent); border: 1px solid color-mix(in srgb, var(--accent-2) 25%, transparent); border-radius: 999px; padding: 0.1rem 0.5rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 12rem; }
  /* View picker — custom themed dropdown */
  .view-menu-wrap { position: relative; }
  .view-btn { display: flex; align-items: center; gap: 0.4rem; background: var(--bg); color: var(--fg-muted); border: 1px solid var(--border-strong); border-radius: 6px; padding: 0.38rem 0.65rem; cursor: pointer; font-size: 0.82rem; white-space: nowrap; }
  .view-btn:hover { border-color: var(--accent-2); color: var(--fg); }
  .view-btn .caret { transition: transform 0.15s; }
  .view-dropdown { position: absolute; top: calc(100% + 4px); left: 0; background: var(--bg-elev); border: 1px solid var(--border-strong); border-radius: 8px; padding: 0.3rem; min-width: 140px; z-index: 200; box-shadow: 0 6px 18px rgba(0,0,0,0.45); }
  .view-dropdown button[role=menuitem] { display: block; width: 100%; padding: 0.38rem 0.65rem; background: transparent; color: var(--fg); border: none; border-radius: 5px; cursor: pointer; text-align: left; font-size: 0.82rem; }
  .view-dropdown button[role=menuitem]:hover { background: var(--bg); color: var(--accent); }
  .vd-section { padding: 0.2rem 0.65rem; color: var(--fg-faint); font-size: 0.68rem; text-transform: uppercase; letter-spacing: 0.06em; }
  .vd-divider { height: 1px; background: var(--border); margin: 0.25rem 0.3rem; }
  /* Save layout button — ghost icon, deliberately NOT blue (blue = new/primary only) */
  .save-layout-btn { display: flex; align-items: center; justify-content: center; width: 34px; height: 34px; background: transparent; color: var(--fg-muted); border: 1px solid var(--border-strong); border-radius: 6px; cursor: pointer; }
  .save-layout-btn:hover { background: var(--bg); color: var(--fg); border-color: var(--accent); }
  /* Primary / spawn button */
  button.primary { background: var(--accent); color: #fff; border: none; border-radius: 6px; padding: 0.42rem 0.95rem; font-weight: 600; cursor: pointer; font-size: 0.88rem; }
  button.spawn-btn { background: #0072F5; color: #fff; padding: 0.5rem 1.1rem; }
  button.secondary { background: var(--bg-elev); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 6px; padding: 0.42rem 0.85rem; cursor: pointer; font-size: 0.88rem; }
  button.icon-btn { background: transparent; color: var(--fg-muted); border: 1px solid var(--border-strong); border-radius: 6px; width: 32px; height: 32px; cursor: pointer; display: flex; align-items: center; justify-content: center; }
  button.icon-btn.small { width: 28px; height: 24px; font-size: 0.7rem; padding: 0; }
  /* #397 P0 — capture-state indicator. ON: subtle accent border;
     OFF: muted + the ✕ glyph signals "shortcuts not capturing". */
  button.icon-btn.capture-state.on { color: var(--accent); border-color: var(--accent); }
  button.icon-btn.capture-state.off { color: var(--fg-faint); border-style: dashed; }
  /* #397 P0 — command palette + cheatsheet overlay. */
  .overlay { position: fixed; inset: 0; background: rgba(0,0,0,0.55); display: flex; align-items: flex-start; justify-content: center; z-index: 1100; padding-top: 8vh; }
  .palette { background: var(--bg-elev); border: 1px solid var(--border-strong); border-radius: 8px; width: min(640px, 90vw); display: flex; flex-direction: column; max-height: 70vh; box-shadow: 0 8px 32px rgba(0,0,0,0.5); }
  .palette-input { background: transparent; color: var(--fg); border: none; border-bottom: 1px solid var(--border); padding: 0.85rem 1rem; font-size: 1.0rem; outline: none; font-family: inherit; }
  .palette-input:focus { border-bottom-color: var(--accent); }
  .palette-list { overflow-y: auto; padding: 0.3rem; display: flex; flex-direction: column; gap: 0.05rem; }
  .palette-item { background: transparent; color: var(--fg); border: 1px solid transparent; border-radius: 4px; padding: 0.5rem 0.7rem; cursor: pointer; display: flex; align-items: center; justify-content: space-between; gap: 1rem; font-size: 0.88rem; text-align: left; }
  .palette-item:hover { background: var(--bg); border-color: var(--border); }
  .pl-label { flex: 1; }
  .pl-key { color: var(--fg-faint); font-family: ui-monospace, monospace; font-size: 0.78rem; }
  .palette-footer { padding: 0.4rem 1rem; border-top: 1px solid var(--border); font-size: 0.72rem; color: var(--fg-faint); text-align: right; }
  .cheatsheet { background: var(--bg-elev); border: 1px solid var(--border-strong); border-radius: 8px; width: min(560px, 90vw); padding: 1rem 1.2rem 0.8rem; max-height: 80vh; overflow-y: auto; box-shadow: 0 8px 32px rgba(0,0,0,0.5); }
  .cs-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 0.6rem; }
  .cs-head h2 { font-size: 1.05rem; margin: 0; color: var(--fg); }
  .cs-table { width: 100%; border-collapse: collapse; font-size: 0.85rem; }
  .cs-table tr { border-bottom: 1px solid var(--border); }
  .cs-k { font-family: ui-monospace, monospace; color: var(--accent); padding: 0.4rem 0.6rem 0.4rem 0; white-space: nowrap; }
  .cs-table td:last-child { padding: 0.4rem 0; color: var(--fg-muted); }
  .cs-section { font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.06em; color: var(--fg-faint); padding: 0.5rem 0 0.2rem; border-top: 1px solid var(--border); border-bottom: none; }
  .cs-note { font-size: 0.78rem; color: var(--fg-muted); margin: 0.6rem 0 0; }
  .cs-note kbd { font-family: ui-monospace, monospace; background: var(--bg); border: 1px solid var(--border); border-radius: 3px; padding: 0.05rem 0.3rem; font-size: 0.78rem; }
  .cs-foot { display: flex; justify-content: flex-end; margin-top: 0.8rem; }
  .font-knob { display: flex; align-items: center; gap: 0.15rem; background: var(--bg); border: 1px solid var(--border); border-radius: 6px; padding: 0.14rem; }
  .font-num { font-size: 0.7rem; color: var(--fg-muted); padding: 0 0.3rem; min-width: 28px; text-align: center; font-family: ui-monospace, monospace; }
  .agent-menu { position: relative; }
  .agent-menu > button { white-space: nowrap; max-width: 220px; overflow: hidden; text-overflow: ellipsis; }
  .agent-menu .dropdown { position: absolute; top: 100%; right: 0; margin-top: 4px; background: var(--bg-elev); border: 1px solid var(--border-strong); border-radius: 6px; padding: 0.25rem; display: flex; flex-direction: column; gap: 0.1rem; z-index: 100; min-width: 140px; box-shadow: 0 4px 12px rgba(0,0,0,0.4); }
  .agent-menu .dropdown button { padding: 0.4rem 0.7rem; background: transparent; color: var(--fg); border: none; border-radius: 4px; cursor: pointer; text-align: left; font-size: 0.82rem; }
  .agent-menu .dropdown button:hover { background: var(--bg); }
  .agent-menu .dropdown button.danger { color: var(--danger); }
  .handoff-row { display: flex; gap: 0.3rem; padding: 0.3rem 0.5rem; align-items: center; }
  .handoff-row select { flex: 1; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 4px; font-size: 0.8rem; padding: 0.2rem 0.3rem; }
  button.primary-sm { background: #0072F5; color: #fff; border: none; border-radius: 4px; padding: 0.2rem 0.5rem; font-size: 0.8rem; cursor: pointer; }
  button.primary-sm:disabled { opacity: 0.4; cursor: default; }
  .canvas { flex: 1; min-height: 0; overflow: hidden; }
  .backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.65); display: flex; align-items: center; justify-content: center; z-index: 1000; backdrop-filter: blur(2px); }
  /* #157 login modal */
  .modal-login { width: min(560px, 94vw); background: var(--bg-elev); border: 1px solid var(--border-strong); border-radius: 10px; padding: 1.3rem 1.5rem; }
  .modal-login h2 { margin: 0 0 0.7rem 0; color: var(--accent); }
  .modal-login textarea { width: 100%; box-sizing: border-box; padding: 0.5rem 0.7rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 6px; font-family: ui-monospace, monospace; font-size: 0.8rem; resize: vertical; }
  .modal-login .error { margin-top: 0.6rem; padding: 0.5rem 0.7rem; background: rgba(255,107,107,0.1); border: 1px solid var(--danger); color: var(--danger); border-radius: 6px; font-size: 0.85rem; }
  .modal-login button.primary { margin-top: 0.9rem; }
  .modal-login .tiny { font-size: 0.78rem; }
  .modal-login code { font-family: ui-monospace, monospace; background: var(--bg-input); padding: 0.1rem 0.35rem; border-radius: 3px; }
  .modal-saveas { width: min(420px, 92vw); background: var(--bg-elev); border: 1px solid var(--border-strong); border-radius: 10px; padding: 1.2rem 1.3rem; }
  .modal-saveas h3 { margin: 0 0 0.7rem 0; color: var(--accent); }
  .modal-saveas input { width: 100%; padding: 0.5rem 0.7rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 6px; font-family: ui-monospace, monospace; }
  .modal-saveas .hint { color: var(--fg-muted); font-size: 0.78rem; margin: 0.5rem 0 1rem 0; }
  .modal-saveas footer { display: flex; justify-content: flex-end; gap: 0.6rem; }
</style>
