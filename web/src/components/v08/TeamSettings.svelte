<!--
  TeamSettings — modal for managing a team. Tabs:
    1. Members  — list, role per member
    2. Canon    — view + edit team CLAUDE.md
    3. Settings — rename, change topology, delete team
-->
<script>
  let { team, members, onClose, onChanged } = $props();
  const API = '/api-v08/v1';
  let tab = $state('members');

  let renameDraft = $state(team?.name || '');
  let topologyDraft = $state(team?.topology || 'hub');
  let confirmDelete = $state(false);

  let canonBody = $state('');
  let canonDraft = $state('');
  let canonEditing = $state(false);

  // Esc closes the modal. Operator request 2026-05-29.
  $effect(() => {
    function onKey(e) { if (e.key === 'Escape') onClose?.(); }
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  });


  async function loadCanon() {
    if (!team?.name) return;
    try {
      const r = await fetch(`${API}/teams/${team.name}/canon`);
      const d = await r.json();
      canonBody = d.body || '';
      canonDraft = d.body || '';
    } catch {}
  }
  $effect(() => { if (tab === 'canon') loadCanon(); });

  async function saveSettings() {
    const body = {};
    if (renameDraft && renameDraft !== team.name) body.new_name = renameDraft;
    if (topologyDraft !== team.topology) body.topology = topologyDraft;
    if (Object.keys(body).length === 0) { onClose?.(); return; }
    await fetch(`${API}/teams/${team.name}`, {
      method: 'PATCH', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    onChanged?.();
    onClose?.();
  }
  async function deleteTeam() {
    await fetch(`${API}/teams/${team.name}`, { method: 'DELETE' });
    onChanged?.();
    onClose?.();
  }
  async function saveCanon() {
    await fetch(`${API}/teams/${team.name}/canon`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ body: canonDraft }),
    });
    canonBody = canonDraft;
    canonEditing = false;
  }
</script>

<div class="backdrop" on:click={onClose}>
  <div class="modal" on:click|stopPropagation>
    <header>
      <h2>team / {team?.name} <small>· {team?.topology} · {members?.length || 0} members</small></h2>
      <button on:click={onClose}>×</button>
    </header>
    <nav class="tabs">
      <button class:active={tab==='members'} on:click={() => tab='members'}>👥 Members</button>
      <button class:active={tab==='canon'} on:click={() => tab='canon'}>📜 Canon</button>
      <button class:active={tab==='settings'} on:click={() => tab='settings'}>⚙ Settings</button>
    </nav>
    <div class="body">
      {#if tab === 'members'}
        <p class="hint">All agents in this team. Click "+ Add agent to this team" to spawn a new agent and auto-join it.</p>
        <table>
          <thead><tr><th>Name</th><th>Role</th><th>Joined</th></tr></thead>
          <tbody>
            {#each (members || []) as m}
              <tr>
                <td>{m.agent_name}</td>
                <td>{m.role}</td>
                <td>{m.joined_at ? new Date(m.joined_at).toLocaleString() : '—'}</td>
              </tr>
            {/each}
            {#if !members?.length}<tr><td colspan="3" class="empty">No memberships yet.</td></tr>{/if}
          </tbody>
        </table>
        <div class="row">
          <button class="primary" on:click={() => { window.dispatchEvent(new CustomEvent('chepherd-add-member', { detail: { team: team.name } })); onClose?.(); }}>+ Add agent to this team</button>
        </div>
      {:else if tab === 'canon'}
        <p class="hint">Team CLAUDE.md — shared rules, prompts, and conventions every member reads each tick.</p>
        {#if canonEditing}
          <textarea bind:value={canonDraft} rows="18"></textarea>
          <div class="row">
            <button class="ghost" on:click={() => { canonDraft = canonBody; canonEditing = false; }}>Cancel</button>
            <button class="primary" on:click={saveCanon}>Save</button>
          </div>
        {:else}
          <pre class="canon">{canonBody || '(no canon yet — click Edit to create one)'}</pre>
          <div class="row"><button class="ghost" on:click={() => (canonEditing = true)}>Edit</button></div>
        {/if}
      {:else if tab === 'settings'}
        <p class="hint">Rename the team or change its topology. Deletion only works if no live agents are still rooted in this team (move or stop them first).</p>
        <div class="settings-grid">
          <label>Team name<input bind:value={renameDraft} /></label>
          <label>Topology
            <select bind:value={topologyDraft}>
              <option value="hub">hub (chepherd in the middle)</option>
              <option value="mesh">mesh (peer-to-peer)</option>
              <option value="custom">custom</option>
            </select>
          </label>
        </div>
        {#if confirmDelete}
          <div class="row confirm-row">
            <span class="confirm-label">Delete team "{team.name}"? Members become unaffiliated.</span>
            <button class="danger" on:click={deleteTeam}>Confirm delete</button>
            <button class="ghost" on:click={() => (confirmDelete = false)}>Cancel</button>
          </div>
        {:else}
          <div class="row">
            <button class="danger" on:click={() => (confirmDelete = true)}>🗑 Delete team</button>
            <span style="flex:1"></span>
            <button class="ghost" on:click={onClose}>Cancel</button>
            <button class="primary" on:click={saveSettings}>Save</button>
          </div>
        {/if}
      {/if}
    </div>
  </div>
</div>

<style>
  .backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.65); display: flex; align-items: center; justify-content: center; z-index: 1000; backdrop-filter: blur(2px); }
  .modal { width: min(820px, 96vw); max-height: 94vh; background: var(--bg-elev); border: 1px solid var(--border-strong); border-radius: 10px; display: flex; flex-direction: column; }
  header { display: flex; align-items: center; padding: 0.85rem 1.2rem; border-bottom: 1px solid var(--border); gap: 0.6rem; }
  h2 { margin: 0; color: var(--accent); flex: 1; }
  h2 small { color: var(--fg-muted); font-weight: normal; }
  header > button { background: transparent; color: var(--fg-muted); border: none; font-size: 1.5rem; cursor: pointer; }
  nav.tabs { display: flex; gap: 0.2rem; padding: 0.4rem 0.7rem; background: var(--bg); border-bottom: 1px solid var(--border); }
  nav.tabs button { padding: 0.4rem 0.85rem; background: transparent; color: var(--fg-muted); border: none; border-radius: 6px; cursor: pointer; }
  nav.tabs button.active { background: var(--bg-elev); color: var(--accent); }
  .body { padding: 1rem 1.2rem; overflow-y: auto; flex: 1; min-height: 280px; }
  .hint { color: var(--fg-muted); margin: 0 0 0.7rem 0; }
  table { width: 100%; border-collapse: collapse; }
  th { text-align: left; color: var(--fg-muted); padding: 0.4rem 0.5rem; border-bottom: 1px solid var(--border); }
  td { padding: 0.4rem 0.5rem; border-bottom: 1px solid var(--border); }
  td.empty { color: var(--fg-faint); text-align: center; padding: 1rem; }
  textarea { width: 100%; padding: 0.55rem 0.7rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 6px; font-family: ui-monospace, monospace; resize: vertical; box-sizing: border-box; }
  pre.canon { background: var(--bg-input); padding: 0.7rem; border-radius: 6px; margin: 0; overflow: auto; white-space: pre-wrap; word-break: break-word; max-height: 50vh; }
  .settings-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 0.4rem 0.7rem; }
  label { display: block; color: var(--fg-muted); text-transform: uppercase; letter-spacing: 0.04em; }
  input, select { width: 100%; padding: 0.4rem 0.55rem; background: var(--bg-input); color: var(--fg); border: 1px solid var(--border-strong); border-radius: 4px; font-family: ui-monospace, monospace; margin-top: 0.15rem; box-sizing: border-box; }
  .row { margin-top: 0.9rem; display: flex; gap: 0.55rem; align-items: center; }
  button.primary { background: var(--accent); color: #000; border: none; border-radius: 6px; padding: 0.45rem 1rem; font-weight: 600; cursor: pointer; }
  button.ghost { background: transparent; color: var(--fg-muted); border: 1px solid var(--border-strong); border-radius: 6px; padding: 0.45rem 0.95rem; cursor: pointer; }
  button.danger { background: transparent; color: var(--danger); border: 1px solid var(--danger); border-radius: 6px; padding: 0.45rem 0.95rem; cursor: pointer; }
  .confirm-row { background: color-mix(in srgb, var(--danger) 8%, transparent); border: 1px solid color-mix(in srgb, var(--danger) 30%, transparent); border-radius: 6px; padding: 0.5rem 0.7rem; gap: 0.6rem; }
  .confirm-label { flex: 1; color: var(--fg); font-size: 0.87rem; }
</style>
