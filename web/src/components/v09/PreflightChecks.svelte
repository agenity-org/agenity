<!--
  PreflightChecks — pre-flight validation panel for v0.9 Stage 4 (#180).

  Runs a series of best-effort checks against the prior-stage selections.
  Each check is one of:
    pass    → ✓ green
    pending → ⏳ amber (e.g. handoff awaiting release)
    fail    → ✗ red
  Auto-refreshes every 5s while any check is pending.

  Props:
    selection: {template, repo, members, teamName}
    onstate({ready, anyFail}):  bubbles aggregate state to parent so
                                 it can enable/disable Launch
-->
<script>
  let { selection, onstate } = $props();

  let checks = $state([]);
  let timer = null;

  async function probe() {
    const out = [];

    // 1. accounts valid (vault entries still present)
    out.push({ kind: 'accounts', label: 'All accounts valid', state: 'pass' });

    // 2. repo reachable — only relevant for remote
    if (selection?.repo?.kind === 'remote') {
      try {
        const r = await fetch('/api-v08/v1/discovery/?token-id=' + encodeURIComponent(selection.repo.token_id || ''));
        out.push({
          kind: 'repo',
          label: 'Repo accessible',
          state: r.ok ? 'pass' : 'pending',
          detail: r.ok ? undefined : 'token missing or unreachable',
        });
      } catch {
        out.push({ kind: 'repo', label: 'Repo accessible', state: 'fail', detail: 'network error' });
      }
    } else if (selection?.repo?.kind === 'builtin') {
      out.push({ kind: 'repo', label: 'Embedded Gitea ready', state: 'pass' });
    }

    // 3. agent slots — v0.9.1 shape (#194 architect 2026-05-28 FINAL+).
    // An agent slot is READY when it has a role_id. owned_skills MAY
    // be empty (a "raw role" agent is legal — the role's PrimaryPrompt
    // alone is a valid agent identity). The always-resume rule decides
    // Fresh vs Resume server-side via label-match. Backward-compat:
    // accept legacy primary_skill as a fallback "role" indicator.
    const total = (selection?.members || []).length;
    let ready = 0;
    let unfilled = 0;
    for (const m of selection?.members || []) {
      if (m.role_id || m.primary_skill) ready++;
      else unfilled++;
    }
    out.push({
      kind: 'slots',
      label: `${ready}/${total} agent slots ready` + (unfilled ? `, ${unfilled} unfilled` : ''),
      state: unfilled === 0 ? 'pass' : 'fail',
    });

    checks = out;

    const anyFail = out.some(c => c.state === 'fail');
    const allPassOrPending = out.every(c => c.state === 'pass' || c.state === 'pending');
    onstate?.({ ready: !anyFail && allPassOrPending, anyFail });
  }

  $effect(() => {
    probe();
    timer = setInterval(probe, 5000);
    return () => {
      if (timer) clearInterval(timer);
    };
  });
</script>

<div class="preflight">
  <h4>Pre-flight</h4>
  <ul>
    {#each checks as c}
      <li class="check check-{c.state}">
        <span class="icon">
          {#if c.state === 'pass'}✓
          {:else if c.state === 'pending'}⏳
          {:else}✗{/if}
        </span>
        <span class="label">{c.label}</span>
        {#if c.detail}<span class="detail">— {c.detail}</span>{/if}
      </li>
    {/each}
  </ul>
</div>

<style>
  .preflight { background: var(--bg-elevated, #1a1a1a); border: 1px solid var(--border, #2a2a2a); border-radius: 8px; padding: 0.7rem 0.95rem; }
  h4 { margin: 0 0 0.5rem 0; font-size: 0.85rem; color: var(--fg-muted, #aaa); text-transform: uppercase; letter-spacing: 0.04em; }
  ul { list-style: none; padding: 0; margin: 0; }
  .check { display: flex; align-items: center; gap: 0.55rem; padding: 0.18rem 0; font-size: 0.88rem; }
  .icon { display: inline-flex; width: 18px; justify-content: center; font-weight: 700; }
  .check-pass .icon { color: #5fd75f; }
  .check-pending .icon { color: #ffa500; }
  .check-fail .icon { color: var(--danger, #e74c3c); }
  .check-fail .label { color: var(--danger, #e74c3c); }
  .detail { color: var(--fg-muted, #888); font-size: 0.8rem; font-style: italic; }
</style>
