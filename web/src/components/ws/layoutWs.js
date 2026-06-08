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
  { id: 'inspector',  label: 'Agent Details', glyph: '◉' },
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

// ---------------- tree constructors ----------------
export function leaf(widget = 'terminal', config = {}) {
  return { kind: 'leaf', id: nextId('leaf'), widget, config };
}
export function hsplit(a, b, ratio = 0.5) { return { kind: 'h', id: nextId('split'), a, b, ratio }; }
export function vsplit(a, b, ratio = 0.5) { return { kind: 'v', id: nextId('split'), a, b, ratio }; }

// A fresh workspace leads with a Sessions pane beside a terminal so the
// operator can immediately reach agents (no fixed roster exists anymore).
export function defaultLayout() { return hsplit(leaf('sessions', {}), leaf('terminal', {}), 0.24); }

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
    const ratio = typeof node.ratio === 'number' ? node.ratio : 0.5;
    const out = { kind: node.kind, id: typeof node.id === 'string' ? node.id : nextId('split'), a, b, ratio };
    if (typeof node._prevRatio === 'number') out._prevRatio = node._prevRatio;
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
    return { kind: node.kind, id: nextId('split'), a, b, ratio };
  }
  return defaultLayout();
}

// ---------------- seed compositions ----------------
// Three editable/deletable starting desktops — the global first-run default
// for every new operator. Just compositions; the operator can rebuild them
// freely. Every seed leads with a Sessions pane (the agent list — there is no
// longer a fixed roster) so agents are reachable out of the box.
//   Work (active on load) — Sessions(16%) | Terminal(62%) | Agent Details(22%).
//     A three-way horizontal split: agent list on the far left, the live
//     terminal of the auto-focused agent in the centre, its Agent Details on
//     the right. Encoded as Sessions | (Terminal | AgentDetails). With the
//     outer ratio 0.16 the right group gets 84%; split that 0.738 so the
//     terminal occupies 0.84·0.738 ≈ 0.62 and details 0.84·0.262 ≈ 0.22.
//   Overview — Sessions(18%) | (Kanban | Events).
//   Conversation — Transcript (large) | Agent Details(24%).
export function seedWorkspaces() {
  return [
    {
      id: nextId('ws'),
      name: 'Work',
      layout: hsplit(
        leaf('sessions', {}),
        hsplit(leaf('terminal', {}), leaf('inspector', {}), 0.738),
        0.16,
      ),
    },
    {
      id: nextId('ws'),
      name: 'Overview',
      layout: hsplit(
        leaf('sessions', {}),
        hsplit(leaf('kanban', {}), leaf('events', {}), 0.6),
        0.18,
      ),
    },
    {
      id: nextId('ws'),
      name: 'Conversation',
      layout: hsplit(leaf('transcript', {}), leaf('inspector', {}), 0.76),
    },
  ];
}

// ---------------- persistence (per-user → localStorage) ----------------
// Keyed so the data survives reloads. We persist the full workspace list,
// the active index, and per-workspace focus/maximize hints.
//
// v3: the global first-run default changed (Work = Sessions|Terminal|Agent
// Details; Overview; Conversation). Bumping the key re-seeds operators who
// still carry an UNTOUCHED v2 seed, while operators who CUSTOMISED their v2
// layout keep it (migrated forward) — see loadWorkspaces' v2 fallback. v2
// itself re-seeded v1 operators (roster → Sessions pane).
const STORE_KEY = 'ws-workspaces-v3';
const ACTIVE_KEY = 'ws-active-v3';
const STORE_KEY_V2 = 'ws-workspaces-v2';
const ACTIVE_KEY_V2 = 'ws-active-v2';

// Structural fingerprint of a tree, id-independent: pane types + split axes +
// rounded ratios. Lets us tell a PRISTINE old seed (re-seed it) from a layout
// the operator edited (migrate it forward).
function fingerprint(node) {
  if (!node || typeof node !== 'object') return '∅';
  if (node.kind === 'leaf') return `L:${node.widget}`;
  const r = Math.round((typeof node.ratio === 'number' ? node.ratio : 0.5) * 100);
  return `${node.kind}(${r},${fingerprint(node.a)},${fingerprint(node.b)})`;
}
function workspacesFingerprint(list) {
  if (!Array.isArray(list)) return '';
  return list.map((w) => `${w?.name || ''}=${fingerprint(w?.layout)}`).join('|');
}
// The exact v2 pristine seed shape (Work/Board/Talk). Generated, not hand-
// typed, so it can't drift from the algebra. Any stored v2 list whose
// fingerprint matches this was never customised → safe to re-seed to v3.
function v2SeedFingerprint() {
  const v2 = [
    { name: 'Work',  layout: hsplit(leaf('sessions', {}), vsplit(leaf('terminal', {}), leaf('inspector', {}), 0.66), 0.22) },
    { name: 'Board', layout: hsplit(leaf('sessions', {}), hsplit(leaf('kanban', {}), leaf('events', {}), 0.6), 0.22) },
    { name: 'Talk',  layout: hsplit(leaf('sessions', {}), leaf('transcript', {}), 0.22) },
  ];
  return workspacesFingerprint(v2);
}

function parseStored(raw) {
  try {
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed) || parsed.length === 0) return null;
    const out = [];
    for (const w of parsed) {
      const layout = sanitizeNode(w?.layout) || defaultLayout();
      out.push({
        id: typeof w?.id === 'string' ? w.id : nextId('ws'),
        name: (typeof w?.name === 'string' && w.name.trim()) ? w.name : 'Workspace',
        layout,
      });
    }
    return out.length ? out : null;
  } catch { return null; }
}

export function loadWorkspaces() {
  try {
    // Post-migration users: just load v3 (custom or freshly-seeded alike).
    const v3 = localStorage.getItem(STORE_KEY);
    if (v3) return parseStored(v3);

    // No v3 yet. Look at the legacy v2 store to decide migrate-vs-reseed.
    const v2raw = localStorage.getItem(STORE_KEY_V2);
    if (!v2raw) return null;                 // brand-new user → seed v3
    const v2list = parseStored(v2raw);
    if (!v2list) return null;                // unparseable → seed v3
    // Untouched v2 default → discard, return null so the caller seeds v3.
    if (workspacesFingerprint(v2list) === v2SeedFingerprint()) return null;
    // Customised v2 layout → migrate it forward (it persists under v3 on the
    // next save). The operator keeps their desktops.
    return v2list;
  } catch { return null; }
}

export function saveWorkspaces(workspaces) {
  try {
    // Strip transient _prevRatio is fine to keep; it's serializable.
    localStorage.setItem(STORE_KEY, JSON.stringify(workspaces));
  } catch {}
}

export function loadActiveId() {
  try { return localStorage.getItem(ACTIVE_KEY) || localStorage.getItem(ACTIVE_KEY_V2) || ''; } catch { return ''; }
}
export function saveActiveId(id) {
  try { localStorage.setItem(ACTIVE_KEY, id || ''); } catch {}
}
