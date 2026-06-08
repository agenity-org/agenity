// layoutWs.js — the WORKSPACES dashboard model.
//
// CORE CONCEPT (OS virtual-desktop, like Windows/Ubuntu workspaces):
//   A WORKSPACE is a named, saved, fully-composed layout — its own
//   recursive split-tree of panes. The operator creates/names/reorders/
//   deletes/duplicates workspaces and switches between them; each is a
//   whole desktop. This subsumes the dock/grid/chat ideas as user-made
//   arrangements.
//
// Two node kinds in a workspace's split-tree (same shape the reused
// CalmPane/CalmLeaf understand, so we can drive them directly):
//   leaf  : { kind:'leaf', id, widget, config }
//   split : { kind:'h'|'v', id, a, b, ratio }   // ratio = size of `a`
//
// PANE TYPES (leaf.widget): sessions | terminal | kanban | events |
//   transcript | inspector | mesh | tasks | mcplog. Terminal leaves carry
//   config.agent. There are NO special fixed regions — the former roster is
//   now the `sessions` pane type and the former inspector is `inspector`, so
//   the whole desktop is one uniform, resizable, content-changeable tree.
//
// This module is ws-local (no collision with calm/layoutTree.js) and owns
// BOTH the tree algebra AND the per-user workspace persistence to
// localStorage. The seed compositions are just starting points — every
// seeded workspace is editable + deletable.

let _seq = 0;
export function nextId(prefix = 'n') {
  _seq += 1;
  return `${prefix}-${Date.now().toString(36)}-${_seq}`;
}

// ---- pane catalogue (single source of truth for menus + sanitize) ----
// Sessions replaces the old fixed roster; MCP Log moved out of Settings to a
// runtime pane. Every entry here is a generic, composable pane the operator
// can put anywhere in the split-tree.
export const PANE_TYPES = [
  { id: 'sessions',   label: 'Sessions',   glyph: '☰' },
  { id: 'terminal',   label: 'Terminal',   glyph: '▦' },
  { id: 'inspector',  label: 'Details', glyph: '◉' },
  { id: 'kanban',     label: 'Kanban',     glyph: '☷' },
  { id: 'events',     label: 'Events',     glyph: '〜' },
  { id: 'transcript', label: 'Transcript', glyph: '✉' },
  { id: 'mesh',       label: 'Mesh',       glyph: '⇄' },
  { id: 'tasks',      label: 'Tasks',      glyph: '☑' },
  { id: 'mcplog',     label: 'MCP Log',    glyph: '⛁' },
];
const KNOWN = new Set(PANE_TYPES.map((p) => p.id));

export function paneMeta(widget) {
  return PANE_TYPES.find((p) => p.id === widget) || PANE_TYPES[0];
}

// ---------------- shared layout constants ----------------
// SESSIONS_W is the FIXED pixel width of the left Sessions pane in EVERY
// seed that has one (Work, Talk) so it reads identically tab-to-tab
// (#3/#4 — operator-reported "Sessions width varies per tab"). It's a
// pixel constant, not a ratio, because ratios scale with the viewport and
// therefore differ between a 16%-Work-split and a 24%-Talk-split.
// DETAILS_W is the matching fixed width for the right Details rail. It is
// deliberately EQUAL to SESSIONS_W so the left (Sessions) and right (Details)
// rails render at identical px — a symmetric frame around the terminal region
// (operator-reported "Details rail wider than Sessions"). Splits carrying
// `fixed:'a'|'b'` + `fixedPx` render that side at a fixed px width (the other
// side flexes) — see WsPane.
export const SESSIONS_W = 260;
export const DETAILS_W = SESSIONS_W;
// Back-compat alias: older call sites referenced AGENT_DETAILS_W. Kept equal
// to DETAILS_W so any lingering reference still yields the symmetric width.
export const AGENT_DETAILS_W = DETAILS_W;

// ---------------- tree constructors ----------------
export function leaf(widget = 'terminal', config = {}) {
  return { kind: 'leaf', id: nextId('leaf'), widget, config };
}
export function hsplit(a, b, ratio = 0.5) { return { kind: 'h', id: nextId('split'), a, b, ratio }; }
export function vsplit(a, b, ratio = 0.5) { return { kind: 'v', id: nextId('split'), a, b, ratio }; }

// fixedSplit makes ONE side a fixed pixel width (the other flexes to fill).
// `which` is 'a' or 'b' — the fixed side. Used for the Sessions / Agent-
// Details rails so they're pixel-identical across every workspace tab. The
// kind is always 'h' (left/right rails). ratio is retained as a sane
// fallback if a consumer ignores the fixed hint.
export function fixedSplit(a, b, which, px) {
  return { kind: 'h', id: nextId('split'), a, b, ratio: which === 'a' ? 0.2 : 0.8, fixed: which, fixedPx: px };
}

// gridOf tiles 1..4 terminal leaves in a BALANCED grid (the auto-managed
// live-agent terminals for the Work view). 1→single, 2→side-by-side,
// 3→one-over-a-pair, 4→2×2. Each config carries the agent name (or {} when
// no agent is available so the pane still renders its "pick agent" state).
// With zero configs it returns ONE empty terminal leaf — the "no agent yet"
// placeholder — so the Work view always shows exactly one terminal pane when
// the team is empty (never the old hardcoded 3).
export function gridOf(configs) {
  const c = (configs && configs.length) ? configs : [{}];
  const L = (cfg) => leaf('terminal', cfg || {});
  if (c.length === 1) return L(c[0]);
  if (c.length === 2) return hsplit(L(c[0]), L(c[1]), 0.5);
  if (c.length === 3) return vsplit(hsplit(L(c[0]), L(c[1]), 0.5), L(c[2]), 0.5);
  // 4 (and any overflow, capped by the caller) → 2×2.
  return vsplit(hsplit(L(c[0]), L(c[1]), 0.5), hsplit(L(c[2]), L(c[3]), 0.5), 0.5);
}

// AUTO_REGION marks the split whose `a` side is the DASHBOARD-MANAGED terminal
// region of the Work view — the centre column between the Sessions (left) and
// Details (right) rails. The dashboard reactively rebuilds that `a` subtree to
// a balanced grid of up-to-4 live, non-shepherd agent terminals as the roster
// changes (count-driven auto-fill). The marker rides on the SPLIT (not a leaf)
// so it survives the subtree being rebuilt out from under it. It is dropped
// the moment the operator manually edits the Work layout (per-workspace manual
// flag) so we never clobber their arrangement.
export const AUTO_REGION = 'work';

// findAutoRegionSplit returns the split node carrying autoRegion === AUTO_REGION
// (the managed terminal region's parent), or null. The managed terminal subtree
// is that split's `a` child.
export function findAutoRegionSplit(node) {
  if (!node || node.kind === 'leaf') return null;
  if (node.autoRegion === AUTO_REGION) return node;
  return findAutoRegionSplit(node.a) || findAutoRegionSplit(node.b);
}

// setAutoRegionChild replaces the `a` child of the auto-region split with
// `subtree` (the freshly-built terminal grid), immutably. No-op (returns the
// tree unchanged) when there is no auto-region split.
export function setAutoRegionChild(tree, subtree) {
  function rec(node) {
    if (!node || node.kind === 'leaf') return node;
    if (node.autoRegion === AUTO_REGION) return { ...node, a: subtree };
    return { ...node, a: rec(node.a), b: rec(node.b) };
  }
  return rec(tree);
}

// A fresh workspace leads with a Sessions pane beside a terminal so the
// operator can immediately reach agents (no fixed roster exists anymore).
// The Sessions rail is the shared fixed width.
export function defaultLayout() { return fixedSplit(leaf('sessions', {}), leaf('terminal', {}), 'a', SESSIONS_W); }

// ---------------- tree algebra (immutable rebuilds → $state fires) ----
export function leaves(node, out = []) {
  if (!node) return out;
  if (node.kind === 'leaf') { out.push(node); return out; }
  leaves(node.a, out);
  leaves(node.b, out);
  return out;
}
export function findLeaf(node, id) {
  if (!node) return null;
  if (node.kind === 'leaf') return node.id === id ? node : null;
  return findLeaf(node.a, id) || findLeaf(node.b, id);
}
export function countLeaves(node) { return leaves(node).length; }

export function splitLeaf(tree, leafId, dir, newWidget = 'terminal', newConfig = {}) {
  const fresh = leaf(newWidget, newConfig);
  function rec(node) {
    if (!node) return node;
    if (node.kind === 'leaf') {
      if (node.id !== leafId) return node;
      return { kind: dir, id: nextId('split'), a: node, b: fresh, ratio: 0.5 };
    }
    return { ...node, a: rec(node.a), b: rec(node.b) };
  }
  return { tree: rec(tree), newId: fresh.id };
}

// replaceLeaf swaps the leaf with `leafId` for an arbitrary subtree
// (`subtree`). Used to graft the auto-opened terminal grid in place of the
// Work seed's placeholder terminal (#3). Immutable rebuild.
export function replaceLeaf(tree, leafId, subtree) {
  function rec(node) {
    if (!node) return node;
    if (node.kind === 'leaf') return node.id === leafId ? subtree : node;
    return { ...node, a: rec(node.a), b: rec(node.b) };
  }
  return rec(tree);
}

export function removeLeaf(tree, leafId) {
  function rec(node) {
    if (!node) return node;
    if (node.kind === 'leaf') return node;
    const a = rec(node.a);
    const b = rec(node.b);
    if (node.a.kind === 'leaf' && node.a.id === leafId) return b;
    if (node.b.kind === 'leaf' && node.b.id === leafId) return a;
    return { ...node, a, b };
  }
  return rec(tree) || defaultLayout();
}

export function setLeafConfig(tree, leafId, config) {
  function rec(node) {
    if (!node) return node;
    if (node.kind === 'leaf') {
      if (node.id !== leafId) return node;
      return { ...node, config: { ...node.config, ...config } };
    }
    return { ...node, a: rec(node.a), b: rec(node.b) };
  }
  return rec(tree);
}
export function setLeafWidget(tree, leafId, widget, config = {}) {
  function rec(node) {
    if (!node) return node;
    if (node.kind === 'leaf') {
      if (node.id !== leafId) return node;
      return { ...node, widget, config };
    }
    return { ...node, a: rec(node.a), b: rec(node.b) };
  }
  return rec(tree);
}
export function setSplitRatio(tree, splitId, ratio) {
  const r = Math.max(0.12, Math.min(0.88, ratio));
  function rec(node) {
    if (!node || node.kind === 'leaf') return node;
    if (node.id === splitId) return { ...node, ratio: r };
    return { ...node, a: rec(node.a), b: rec(node.b) };
  }
  return rec(tree);
}
// setSplitFixedPx updates the pixel width of a fixed-rail split (the value
// set by dragging a fixed-rail divider). Clamped to a sane range.
export function setSplitFixedPx(tree, splitId, px) {
  const p = Math.max(140, Math.min(640, px));
  function rec(node) {
    if (!node || node.kind === 'leaf') return node;
    if (node.id === splitId && (node.fixed === 'a' || node.fixed === 'b')) return { ...node, fixedPx: p };
    return { ...node, a: rec(node.a), b: rec(node.b) };
  }
  return rec(tree);
}

export function parentOf(tree, leafId) {
  function rec(node) {
    if (!node || node.kind === 'leaf') return null;
    if (node.a?.kind === 'leaf' && node.a.id === leafId) return { split: node, side: 'a' };
    if (node.b?.kind === 'leaf' && node.b.id === leafId) return { split: node, side: 'b' };
    return rec(node.a) || rec(node.b);
  }
  return rec(tree);
}

// Collapse a leaf along its parent split axis: drive the split ratio to an
// extreme; expand restores the saved pre-collapse ratio. (#1 arrow-collapse
// for panes inside a workspace.)
export function collapseLeaf(tree, leafId, makeSmall) {
  const p = parentOf(tree, leafId);
  if (!p) return tree;
  const { split, side } = p;
  function rec(node) {
    if (!node || node.kind === 'leaf') return node;
    if (node.id === split.id) {
      // Fixed-px rail: collapse/expand by toggling the pixel width of the
      // fixed side (the renderer ignores ratio for fixed splits, so driving
      // ratio alone would do nothing). Only the FIXED side collapses.
      if (node.fixed === 'a' || node.fixed === 'b') {
        if (side !== node.fixed) return node;   // flex side can't collapse a rail
        if (makeSmall) {
          return { ...node, fixedPx: 12, _prevFixedPx: typeof node.fixedPx === 'number' ? node.fixedPx : 260 };
        }
        const prevPx = typeof node._prevFixedPx === 'number' ? node._prevFixedPx : 260;
        const { _prevFixedPx, ...rest } = node;
        return { ...rest, fixedPx: prevPx };
      }
      if (makeSmall) {
        const ratio = side === 'a' ? 0.04 : 0.96;
        return { ...node, ratio, _prevRatio: node.ratio ?? 0.5 };
      }
      const prev = typeof node._prevRatio === 'number' ? node._prevRatio : (node.ratio ?? 0.5);
      const ratio = prev > 0.9 || prev < 0.1 ? 0.5 : prev;
      const { _prevRatio, ...rest } = node;
      return { ...rest, ratio };
    }
    return { ...node, a: rec(node.a), b: rec(node.b) };
  }
  return rec(tree);
}
export function collapsedSide(split) {
  if (!split || split.kind === 'leaf') return '';
  // Fixed-px rail: collapsed when the fixed side is driven to its sliver px.
  if (split.fixed === 'a' || split.fixed === 'b') {
    return (typeof split.fixedPx === 'number' && split.fixedPx <= 20) ? split.fixed : '';
  }
  const r = split.ratio ?? 0.5;
  if (r <= 0.06) return 'a';
  if (r >= 0.94) return 'b';
  return '';
}

// ---------------- sanitize (defend persistence loads) ----------------
export function sanitizeNode(node) {
  if (!node || typeof node !== 'object') return null;
  if (node.kind === 'leaf') {
    const widget = KNOWN.has(node.widget) ? node.widget : 'terminal';
    return { kind: 'leaf', id: typeof node.id === 'string' ? node.id : nextId('leaf'), widget, config: (node.config && typeof node.config === 'object') ? node.config : {} };
  }
  if (node.kind === 'h' || node.kind === 'v') {
    const a = sanitizeNode(node.a);
    const b = sanitizeNode(node.b);
    if (!a && !b) return null;
    if (!a) return b;
    if (!b) return a;
    // Clamp the ratio away from the degenerate extremes (0 / 1 / NaN /
    // negative) that would render ONE side at zero size → the "View is empty
    // / unreadable" / blank-pane bug (#3). The [0.02,0.98] band still
    // preserves the deliberate 0.04/0.96 collapse values.
    let ratio = typeof node.ratio === 'number' && isFinite(node.ratio) ? node.ratio : 0.5;
    ratio = Math.max(0.02, Math.min(0.98, ratio));
    const out = { kind: node.kind, id: typeof node.id === 'string' ? node.id : nextId('split'), a, b, ratio };
    if (typeof node._prevRatio === 'number') out._prevRatio = node._prevRatio;
    // Preserve the fixed-pixel rail hint across persistence loads so the
    // Sessions / Agent-Details widths survive a reload (#3/#4).
    if ((node.fixed === 'a' || node.fixed === 'b') && typeof node.fixedPx === 'number') {
      out.fixed = node.fixed; out.fixedPx = node.fixedPx;
      if (typeof node._prevFixedPx === 'number') out._prevFixedPx = node._prevFixedPx;
    }
    // Preserve the auto-managed-terminal-region marker across reloads so the
    // count-driven Work auto-fill keeps targeting the right split (#4).
    if (node.autoRegion === AUTO_REGION) out.autoRegion = AUTO_REGION;
    return out;
  }
  return null;
}

// Deep-clone a tree with FRESH ids on every node. Used when duplicating a
// workspace: the copy is an independent desktop, so its node ids must not
// collide with the source (per-workspace focus/maximize keys by leaf id, and
// findLeaf returns the first id match — colliding ids would cross-link them).
export function cloneTreeFreshIds(node) {
  if (!node || typeof node !== 'object') return null;
  if (node.kind === 'leaf') {
    const widget = KNOWN.has(node.widget) ? node.widget : 'terminal';
    return { kind: 'leaf', id: nextId('leaf'), widget, config: { ...(node.config || {}) } };
  }
  if (node.kind === 'h' || node.kind === 'v') {
    const a = cloneTreeFreshIds(node.a);
    const b = cloneTreeFreshIds(node.b);
    if (!a && !b) return defaultLayout();
    if (!a) return b;
    if (!b) return a;
    const ratio = typeof node.ratio === 'number' ? node.ratio : 0.5;
    const out = { kind: node.kind, id: nextId('split'), a, b, ratio };
    if ((node.fixed === 'a' || node.fixed === 'b') && typeof node.fixedPx === 'number') {
      out.fixed = node.fixed; out.fixedPx = node.fixedPx;
    }
    if (node.autoRegion === AUTO_REGION) out.autoRegion = AUTO_REGION;
    return out;
  }
  return defaultLayout();
}

// workSeedLayout builds the Work view's split-tree:
//   Sessions(fixed 260px) | [ AUTO-REGION: terminals | Details(fixed 260px) ]
// Both rails are FIXED px and EQUAL width (SESSIONS_W === DETAILS_W) so they
// frame the centre symmetrically across every tab. The centre column (the
// auto-region split's `a` child) starts as a SINGLE empty terminal — the
// "no agent yet" placeholder — and the dashboard reactively rebuilds it into a
// balanced grid of up-to-4 live-agent terminals as the roster grows
// (count-driven), until the operator manually edits the Work layout. The inner
// split carries autoRegion === AUTO_REGION so the dashboard can find + rebuild
// the region without disturbing either rail.
function workSeedLayout() {
  const inner = fixedSplit(leaf('terminal', {}), leaf('inspector', {}), 'b', DETAILS_W);
  inner.autoRegion = AUTO_REGION;
  return fixedSplit(leaf('sessions', {}), inner, 'a', SESSIONS_W);
}

// terminalSeedLayout is a FULL-WIDTH pure auto-region terminal grid (no rails).
// The whole tab is the count-driven live-agent grid — the split's `a` child is
// the managed grid, `b` is a thin zero-width sentinel the renderer collapses so
// the grid fills the tab. We mark a wrapping split (not a bare leaf) so the
// autoRegion marker can ride a split and survive the subtree being rebuilt.
function terminalSeedLayout() {
  // Wrap the managed grid as the `a` side of a vertical split whose `b` is a
  // tiny collapsed events strip; this gives the autoRegion marker a split to
  // ride while keeping the terminals dominant. Operators can grow `b` if they
  // want a log under the grid.
  const inner = vsplit(leaf('terminal', {}), leaf('events', {}), 0.82);
  inner.autoRegion = AUTO_REGION;
  return inner;
}

// ---------------- seed compositions ----------------
// The editable/deletable starting desktops — the global first-run default for
// EVERY operator (installed fresh on the v6 re-seed; see loadWorkspaces). Just
// compositions; the operator can rebuild them freely. Work + Talk lead with a
// Sessions pane (the agent list — there is no longer a fixed roster) so agents
// are reachable out of the box; Terminal is a pure agent wall and Board is the
// planning surface.
//   Work (active on load) — Sessions(260px) | terminals | Details(260px).
//     Agent list on the far left, the dashboard-managed live-terminal region
//     in the centre (1 pane empty, growing to a balanced up-to-4 grid as the
//     team grows), the focused agent's Details on the right. Both rails are
//     fixed + EQUAL width (SESSIONS_W === DETAILS_W).
//   Terminal — a full-width pure auto-region terminal grid (same count-driven
//     centre, no rails) for an at-a-glance wall of live agents.
//   Talk — Sessions(260px) | Transcript. Same Sessions rail width as Work.
//   Board — Kanban | (Events / MCP Log).
export function seedWorkspaces() {
  return [
    {
      id: nextId('ws'),
      name: 'Work',
      layout: workSeedLayout(),
    },
    {
      id: nextId('ws'),
      name: 'Terminal',
      layout: terminalSeedLayout(),
    },
    {
      id: nextId('ws'),
      name: 'Talk',
      // Talk gets the SAME fixed Sessions rail as Work, then a large
      // Transcript filling the rest.
      layout: fixedSplit(
        leaf('sessions', {}),
        leaf('transcript', {}),
        'a', SESSIONS_W,
      ),
    },
    {
      id: nextId('ws'),
      name: 'Board',
      // Kanban on the left, Events over MCP Log on the right.
      layout: hsplit(
        leaf('kanban', {}),
        vsplit(leaf('events', {}), leaf('mcplog', {}), 0.5),
        0.62,
      ),
    },
  ];
}

// ---------------- persistence (per-user → localStorage) ----------------
// Keyed so the data survives reloads. We persist the full workspace list,
// the active index, and per-workspace focus/maximize hints.
//
// v6: FORCE-RESEED to the canonical dynamic default for EVERY operator. The
// pre-v6 "migrate the customised local layout forward" path is the bug: an
// operator's hand-arranged / migrated-forward layout carries NO
// autoRegion==='work' marker, so syncAutoTerminalRegions skips it and the
// terminal grid stays frozen (the operator's reported "stuck at 3 panes"). On
// v6 absence we therefore ALWAYS install the fresh canonical seed (Work /
// Terminal / Talk / Board — all dynamic where it matters) and DROP the
// migrate-forward branches entirely. The operator's durable, server-saved
// named views are untouched and still loadable separately via the Views menu.
// v5 was equal-width rails + count-driven region but only ever reached
// fresh-cleared users; v4..v2 are the older shapes whose migrate-forward path
// we are now deliberately abandoning.
const STORE_KEY = 'ws-workspaces-v6';
const ACTIVE_KEY = 'ws-active-v6';
const ACTIVE_KEY_V5 = 'ws-active-v5';
const ACTIVE_KEY_V4 = 'ws-active-v4';
const ACTIVE_KEY_V3 = 'ws-active-v3';
const ACTIVE_KEY_V2 = 'ws-active-v2';

// DEFAULT_VIEW_KEY persists the NAME of a saved view the operator marked as
// their startup default (#3). When set + that view loads cleanly it takes
// precedence over the hardcoded seeds on first paint.
const DEFAULT_VIEW_KEY = 'ws-default-view-v4';
export function loadDefaultViewName() { try { return localStorage.getItem(DEFAULT_VIEW_KEY) || ''; } catch { return ''; } }
export function saveDefaultViewName(name) { try { if (name) localStorage.setItem(DEFAULT_VIEW_KEY, name); else localStorage.removeItem(DEFAULT_VIEW_KEY); } catch {} }

function parseStored(raw) {
  try {
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed) || parsed.length === 0) return null;
    const out = [];
    for (const w of parsed) {
      const layout = sanitizeNode(w?.layout) || defaultLayout();
      const ws = {
        id: typeof w?.id === 'string' ? w.id : nextId('ws'),
        name: (typeof w?.name === 'string' && w.name.trim()) ? w.name : 'Workspace',
        layout,
      };
      // workManual: once the operator manually edits a Work-style layout, the
      // count-driven terminal auto-fill (#4) stops touching it. Persist the
      // flag so the arrangement survives a reload as the operator left it.
      if (w?.workManual === true) ws.workManual = true;
      out.push(ws);
    }
    return out.length ? out : null;
  } catch { return null; }
}

// sanitizeWorkspaceList takes a raw workspaces array (e.g. a saved-view blob
// loaded from the server) and returns the same sanitized/migrated shape that
// loadWorkspaces produces from localStorage — valid layouts, ids backfilled,
// names defaulted. Returns null when the input isn't a usable list. Shared so
// server-loaded named views go through the exact same algebra as local ones.
export function sanitizeWorkspaceList(list) {
  // Accept the canonical array shape AND tolerate two legacy/edge shapes a
  // saved-view blob can take so a loaded view ALWAYS renders (#3 empty-view
  // fix): an object wrapper `{workspaces:[…]}` and a single workspace object
  // `{id,name,layout}`. Anything else → null (caller flashes + keeps current).
  if (list && !Array.isArray(list) && typeof list === 'object') {
    if (Array.isArray(list.workspaces)) list = list.workspaces;
    else if (list.layout || list.name) list = [list];
  }
  if (!Array.isArray(list) || list.length === 0) return null;
  return parseStored(JSON.stringify(list));
}

export function loadWorkspaces() {
  try {
    // v6 FORCE-RESEED. Only a v6 store is honoured: a v6 store is EITHER the
    // freshly-installed canonical seed OR an arrangement the operator made
    // AFTER v6 shipped (both correct, both carry the autoRegion marker on the
    // Work/Terminal tabs). When there is NO v6 store yet — including every
    // operator carrying a stale v5/v4/v3/v2 layout — we return null so the
    // caller installs the fresh canonical seed. We deliberately DROP the
    // pre-v6 migrate-forward path: those layouts are the static, marker-less
    // trees that froze the terminal grid. The operator's durable named views
    // (server-side) are unaffected and still loadable from the Views menu.
    const v6 = localStorage.getItem(STORE_KEY);
    if (v6) return parseStored(v6);
    return null;   // no v6 store → caller seeds the canonical dynamic default
  } catch { return null; }
}

export function saveWorkspaces(workspaces) {
  try {
    // Strip transient _prevRatio is fine to keep; it's serializable.
    localStorage.setItem(STORE_KEY, JSON.stringify(workspaces));
  } catch {}
}

export function loadActiveId() {
  try { return localStorage.getItem(ACTIVE_KEY) || localStorage.getItem(ACTIVE_KEY_V5) || localStorage.getItem(ACTIVE_KEY_V4) || localStorage.getItem(ACTIVE_KEY_V3) || localStorage.getItem(ACTIVE_KEY_V2) || ''; } catch { return ''; }
}
export function saveActiveId(id) {
  try { localStorage.setItem(ACTIVE_KEY, id || ''); } catch {}
}
