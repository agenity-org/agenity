// agentIdentity.js — THE single source of truth for an agent's visual
// identity across the dashboard (#694, UX-4 of #690).
//
// Every surface (rail row, terminal tab, transcript chip, inspector
// header) calls agentIdentity() — never reimplements color/icon logic —
// so the same agent looks identical everywhere.
//
// Color: deterministic fnv1a(name) → 8-bucket Okabe-Ito palette
// (colorblind-safe; values tuned for the dark theme). Stable across
// reloads + hosts, zero config. Collision policy: two names MAY share a
// bucket (8 hues, N agents) — that's accepted because color is never
// the only signal: the icon + name are always rendered adjacent (a11y),
// so colliding agents stay distinguishable.
//
// Icon: operator override (session.icon, set via PATCH
// /api/v1/sessions/{name}/icon) wins; else a role default; else ●.

// Okabe-Ito, lightened for dark backgrounds.
export const IDENTITY_PALETTE = [
  '#e69f00', // orange
  '#56b4e9', // sky
  '#2fbf8f', // green (lightened bluish-green)
  '#f0e442', // yellow
  '#4f9dd9', // blue (lightened)
  '#e25d33', // vermillion
  '#cc79a7', // purple-pink
  '#9a9a9a', // grey
];

// Icon by SPECIALTY (the agent's job) — each distinct specialty gets a
// distinct, RELEVANT glyph. Keyed in priority: role_id → agent-type →
// name-keyword → team-role. Sessions are almost all role:'worker' with the
// real job in role_id / the descriptive name, so keying on `role` alone
// collapsed everyone to ⚒ — the bug the operator flagged.
const SPECIALTY_ICONS = {
  'tech-lead': '♛', lead: '♛', orchestrator: '⎈', 'scrum-master': '⚑',
  'product-owner': '◈', architect: '△',
  'code-reviewer': '⚖', reviewer: '⚖',
  'frontend-developer': '❖', frontend: '❖',
  'backend-developer': '⚙', backend: '⚙',
  'full-stack-developer': '⬢', 'full-stack': '⬢', fullstack: '⬢',
  'devops-sre': '∞', devops: '∞', sre: '∞',
  'security-engineer': '⚿', security: '⚿',
  'qa-engineer': '◎', qa: '◎',
  'data-engineer': '⛁', data: '⛁', 'ml-engineer': '⊛', ml: '⊛',
  'technical-writer': '✎', writer: '✎',
  designer: '◐', shepherd: '✻',
  generalist: '⚒', worker: '⚒', implementer: '⚒',
};

// Name-keyword fallback (ordered; first match wins) — for descriptive names
// like "code-reviewer-1" / "full-stack" / "devops-2" when role_id is absent.
const NAME_ICON_RULES = [
  [/lead|orchestr/, '♛'], [/scrum/, '⚑'], [/product|owner/, '◈'],
  [/architect|\barch\b/, '△'], [/review|critic/, '⚖'],
  [/full.?stack/, '⬢'], [/front|\bfe\b|\bui\b/, '❖'], [/back|\bbe\b|server/, '⚙'],
  [/devops|\bsre\b|infra|\bops\b/, '∞'], [/secur|\bsec\b/, '⚿'],
  [/\bqa\b|test/, '◎'], [/\bml\b|model|neural/, '⊛'], [/data|etl/, '⛁'],
  [/writer|docs|scribe/, '✎'], [/design/, '◐'], [/shepherd|watch|prophet/, '✻'],
  [/implement|build|\bdev\b|worker|coder/, '⚒'],
];

function iconFor(name, role, roleId, agentType) {
  const k = (s) => SPECIALTY_ICONS[String(s || '').toLowerCase()];
  const direct = k(roleId) || k(agentType);
  if (direct) return direct;
  const n = String(name || '').toLowerCase();
  for (const [re, glyph] of NAME_ICON_RULES) if (re.test(n)) return glyph;
  return k(role) || '●';
}

// #709.7 — roster-ordered color assignment. Hashing collides in small
// teams (~30% for 3 agents over 8 buckets), breaking the operator's
// literal ask ("different team members in different colors"). Callers
// with roster knowledge (Workspace refresh) call registerRoster() with
// a STABLY-ORDERED list (created_at, then name); the first 8 get
// guaranteed-distinct palette slots. Names beyond 8 — and authors never
// registered (e.g. historical transcript handles) — fall back to the
// deterministic hash.
const rosterColor = new Map();
export function registerRoster(names) {
  rosterColor.clear();
  (names || []).slice(0, IDENTITY_PALETTE.length).forEach((n, i) => {
    rosterColor.set(n, IDENTITY_PALETTE[i]);
  });
}

export function fnv1a(str) {
  let h = 0x811c9dc5;
  for (let i = 0; i < (str || '').length; i++) {
    h ^= str.charCodeAt(i);
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h >>> 0;
}

export function agentColor(name) {
  const assigned = rosterColor.get(name);
  if (assigned) return assigned;
  return IDENTITY_PALETTE[fnv1a(name) % IDENTITY_PALETTE.length];
}

// agentIdentity(agentOrName, role?) → { color, icon, name }
// Accepts a session object ({name, role, icon, agent, external}) or a
// bare name string (+ optional role) for surfaces that only have text
// (e.g. transcript authors that aren't live sessions).
export function agentIdentity(agentOrName, role = '') {
  const isObj = agentOrName && typeof agentOrName === 'object';
  const name = isObj ? (agentOrName.name || '') : (agentOrName || '');
  const r = String((isObj ? agentOrName.role : role) || '').toLowerCase();
  const external = isObj && (agentOrName.agent === 'external-a2a' || agentOrName.external);
  const roleId = isObj ? (agentOrName.role_id || '') : '';
  const agentType = isObj ? (agentOrName.agent || '') : '';
  const override = isObj ? (agentOrName.icon || '') : '';
  const icon = override || (external ? '⇄' : iconFor(name, r, roleId, agentType));
  return { name, color: agentColor(name), icon };
}
