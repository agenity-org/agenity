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

// Default icon by role — lead ♛ · architect △ · reviewer ⚖ · worker ⚒ ·
// shepherd ✻ · qa ◎ · external/hub ⇄ (per the #690 spec).
const ROLE_ICONS = {
  'tech-lead': '♛',
  'scrum-master': '♛',
  'orchestrator': '♛',
  'product-owner': '♛',
  'architect': '△',
  'code-reviewer': '⚖',
  'reviewer': '⚖',
  'worker': '⚒',
  'generalist': '⚒',
  'full-stack-developer': '⚒',
  'frontend-developer': '⚒',
  'backend-developer': '⚒',
  'devops-sre': '⚒',
  'security-engineer': '⚖',
  'shepherd': '✻',
  'qa': '◎',
  'qa-engineer': '◎',
};

export function fnv1a(str) {
  let h = 0x811c9dc5;
  for (let i = 0; i < (str || '').length; i++) {
    h ^= str.charCodeAt(i);
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h >>> 0;
}

export function agentColor(name) {
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
  const override = isObj ? (agentOrName.icon || '') : '';
  const icon = override || (external ? '⇄' : (ROLE_ICONS[r] || '●'));
  return { name, color: agentColor(name), icon };
}
