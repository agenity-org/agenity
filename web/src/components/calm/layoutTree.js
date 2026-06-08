// layoutTree.js — calm dashboard's split-tree model + helpers.
//
// The "calm" workspace is a recursive tree of nodes describing the
// CENTER focus stage. Two node kinds:
//
//   leaf  : { kind:'leaf', id, widget, config }
//   split : { kind:'h'|'v', id, a, b, ratio }   // ratio = size of `a` (0..1)
//
// widget ∈ 'terminal' | 'transcript' | 'inspector'
//
// This is intentionally calm-local (no collision with v08's layout
// shape) so the two dashboards can coexist. The terminal widget reuses
// the existing WidgetTerminal.svelte which reads node.config.agent, so
// our leaf config matches that contract.

let _seq = 0;
export function nextId(prefix = 'n') {
  _seq += 1;
  return `${prefix}-${Date.now().toString(36)}-${_seq}`;
}

export function leaf(widget = 'terminal', config = {}) {
  return { kind: 'leaf', id: nextId('leaf'), widget, config };
}

export function defaultLayout() {
  return leaf('terminal', {});
}

// Depth-first list of leaf nodes.
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

// Split a leaf in-place into a new split with the original leaf as `a`
// and a fresh leaf as `b`. Returns the NEW tree (immutably rebuilt so
// Svelte $state reactivity fires) plus the new leaf id.
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

// Remove a leaf; its sibling collapses up to take the split's place.
export function removeLeaf(tree, leafId) {
  function rec(node) {
    if (!node) return node;
    if (node.kind === 'leaf') return node;
    const a = rec(node.a);
    const b = rec(node.b);
    // Direct child is the target leaf → collapse to the sibling.
    if (node.a.kind === 'leaf' && node.a.id === leafId) return b;
    if (node.b.kind === 'leaf' && node.b.id === leafId) return a;
    return { ...node, a, b };
  }
  const out = rec(tree);
  // Never allow an empty tree.
  return out || defaultLayout();
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

// Find the immediate parent split of a leaf + which side it sits on.
// Returns { split, side } where side ∈ 'a'|'b', or null if the leaf is
// the root (no parent) or not found.
export function parentOf(tree, leafId) {
  function rec(node) {
    if (!node || node.kind === 'leaf') return null;
    if (node.a?.kind === 'leaf' && node.a.id === leafId) return { split: node, side: 'a' };
    if (node.b?.kind === 'leaf' && node.b.id === leafId) return { split: node, side: 'b' };
    return rec(node.a) || rec(node.b);
  }
  return rec(tree);
}

// Collapse a leaf toward (or away from) its sibling along the parent's
// split axis. `dir` ∈ 'a' (shrink so the leaf's own side gets smaller)
// or 'b'. We model collapse by driving the parent split's ratio to a
// near-zero/near-one extreme; expand restores 0.5. We persist the
// pre-collapse ratio on the split as `_prevRatio` so expand can restore.
export function collapseLeaf(tree, leafId, makeSmall) {
  const p = parentOf(tree, leafId);
  if (!p) return tree;
  const { split, side } = p;
  // Determine the target ratio. ratio = size of `a`.
  // If the collapsing leaf is on side 'a' and we shrink it → ratio low.
  // If on side 'b' and we shrink it → ratio high (a takes the room).
  function rec(node) {
    if (!node || node.kind === 'leaf') return node;
    if (node.id === split.id) {
      const prev = typeof node._prevRatio === 'number' ? node._prevRatio : (node.ratio ?? 0.5);
      let ratio;
      if (makeSmall) {
        ratio = side === 'a' ? 0.04 : 0.96;
        return { ...node, ratio, _prevRatio: node.ratio ?? 0.5 };
      }
      // expand / restore
      ratio = prev > 0.9 || prev < 0.1 ? 0.5 : prev;
      const { _prevRatio, ...rest } = node;
      return { ...rest, ratio };
    }
    return { ...node, a: rec(node.a), b: rec(node.b) };
  }
  return rec(tree);
}

// Is this leaf currently collapsed (its parent split driven to an
// extreme)? Returns 'a'|'b' indicating which child is the small one, or
// '' if not collapsed.
export function collapsedSide(split) {
  if (!split || split.kind === 'leaf') return '';
  const r = split.ratio ?? 0.5;
  if (r <= 0.06) return 'a';
  if (r >= 0.94) return 'b';
  return '';
}

export function countLeaves(node) {
  return leaves(node).length;
}

// Sanitise a tree loaded from persistence (drop unknown widgets, fix
// dangling configs). Falls back to a clean default on garbage.
const KNOWN = new Set(['terminal', 'transcript', 'inspector']);
export function sanitize(node) {
  if (!node || typeof node !== 'object') return null;
  if (node.kind === 'leaf') {
    const widget = KNOWN.has(node.widget) ? node.widget : 'terminal';
    return { kind: 'leaf', id: node.id || nextId('leaf'), widget, config: node.config || {} };
  }
  if (node.kind === 'h' || node.kind === 'v') {
    const a = sanitize(node.a);
    const b = sanitize(node.b);
    if (!a && !b) return null;
    if (!a) return b;
    if (!b) return a;
    const ratio = typeof node.ratio === 'number' ? node.ratio : 0.5;
    return { kind: node.kind, id: node.id || nextId('split'), a, b, ratio };
  }
  return null;
}
