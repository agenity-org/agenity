<!--
  SpawnWizardV9 — 6-stage v0.9 wizard (#237 — operator-driven restructure
  2026-05-30 inserting Agent Types & Models between Skills and Accounts).

  Stages (filename stable; the number is metadata in this composer):
    1 Shape         — Stage1Shape      template gallery
    2 Repo          — Stage2Repo       where the code lives
    3 Skills        — Stage3Skills     per-team role × skill matrix override
    4 Agent Types   — Stage4AgentTypes per-agent agent_type + model picker (NEW #237)
    5 Accounts      — Stage4Accounts   per-agent-type vault picker + Connect-Claude
                                       (file name stable; stage number bumped 4 → 5)
    6 Launch        — Stage5Launch     spawn (file name stable; stage number 5 → 6)

  State threaded between stages:
    templateId / template / repo / teamName  (1+2)
    agents     — the slot roster (label/role_id) carried through
    skillOverrides[roleID] → string[] of skill IDs (set in Stage 3)
                  empty for a role = use that role's global default_skills
    agentTypeOverrides[agentLabel] → agent type slug (Stage 4 — NEW)
    agentModelOverrides[agentLabel] → model name (Stage 4 — NEW)
    typeAccounts[agentType] → vault entry id  (set in Stage 5 Accounts)
    agentAccountOverrides[agentLabel] → vault entry id  (Stage 5 advanced)

  Hard rules:
    - Per-team override semantics: Stage 3 edits NEVER mutate the global
      role catalog. Only the dashboard 🎮 roles widget does that.
    - Launch is blocked until every agent has a resolvable account.
    - Per-agent type-changes in Stage 4 do NOT auto-clear typeAccounts
      (architect call 2026-05-30): if the existing type-level cred matches
      the new type's compatibility map it stays valid; if not, the
      per-agent slot shows empty and operator gets the "Next disabled"
      signal naturally.

  Props:
    onclose(): close handler from Workspace
-->
<script>
  import WizardStepper from './WizardStepper.svelte';
  import Stage1Shape from './Stage1Shape.svelte';
  import Stage2Repo from './Stage2Repo.svelte';
  import Stage3Skills from './Stage3Skills.svelte';
  import Stage4AgentTypes from './Stage4AgentTypes.svelte';
  import Stage4Accounts from './Stage4Accounts.svelte';
  import Stage5Launch from './Stage5Launch.svelte';

  let { onclose } = $props();

  let current = $state(1);
  let templateId = $state('');
  let template = $state(null);
  let repo = $state(null);
  let agents = $state([]);
  let teamName = $state('');
  let saveAsRecipe = $state(false);

  // Stage 3 output. Map of roleID → skillIDs[]. Absent key means
  // "use the role's global default_skills at Launch time".
  let skillOverrides = $state({});

  // Stage 4 (NEW #237) output. Per-agent override of agent_type +
  // model. Both default to template values; operator can override
  // per-agent in Stage 4.
  let agentTypeOverrides = $state({});
  let agentModelOverrides = $state({});

  // Stage 5 Accounts output. typeAccounts is the per-agent-type
  // default; agentAccountOverrides is the per-individual-agent
  // override (advanced mode in Stage 5).
  let typeAccounts = $state({});
  let agentAccountOverrides = $state({});

  function next() { if (current < 6) current += 1; }
  function back() { if (current > 1) current -= 1; }
  function jumpTo(step) { if (step >= 1 && step < current) current = step; }

  // #237 — `agentTypeFor` honors Stage 4's per-agent override before
  // falling back to the template's `agent_type`. Used by Stage 5
  // Accounts to filter vault entries by the right type, and by
  // Stage 6 Launch to populate the spawn POST body.
  function agentTypeFor(a) {
    return agentTypeOverrides[a.label] || a.agent_type || 'claude-code';
  }

  function accountFor(a) {
    return agentAccountOverrides[a.label] || typeAccounts[agentTypeFor(a)] || '';
  }

  // #741 — OAuth host-mount flavors (gemini-cli / qwen-code / copilot) get
  // their credential from the host login dir the backend auto-mounts
  // (~/.gemini / ~/.qwen / ~/.config/gh), so they need NO vault account and
  // must NOT block Launch. Keep this list in sync with Stage4Accounts'
  // TYPE_GUIDANCE oauth entries. claude-code / codex-cli / aider / opencode
  // still require a concrete account.
  const OAUTH_HOST_MOUNT_TYPES = new Set(['gemini-cli', 'qwen-code', 'copilot']);

  const allAgentsHaveAccount = $derived.by(() =>
    agents.length > 0 &&
    agents.every(a => !!accountFor(a) || OAUTH_HOST_MOUNT_TYPES.has(agentTypeFor(a)))
  );

  const canAdvance = $derived.by(() => {
    switch (current) {
      case 1: return !!templateId;
      case 2: return !!repo;
      case 3: return agents.length > 0 && !!teamName;
      case 4: return agents.length > 0;       // Agent Types & Models — defaults satisfy
      case 5: return allAgentsHaveAccount;    // block Launch until all accounts set
      case 6: return true;
      default: return false;
    }
  });

  function selectTemplate(id, t) {
    templateId = id;
    template = t;
    if (!template || !template.slots || template.slots.length === 0) {
      agents = [];
    }
  }

  function launchedThenClose() { onclose?.(); }

  // #252 — Launch primary CTA hoisted from Stage 6's body into the
  // outer wizard footer so it's always visible regardless of stage
  // body scroll. Stage5Launch writes into this controller via
  // bind:controller; the footer's onclick invokes controller.launch().
  let launchController = $state({
    label: '⚡ Launch',
    canLaunch: false,
    launching: false,
    launch: () => {},
  });

  const stepLabels = [
    { label: 'Shape' },
    { label: 'Repo' },
    { label: 'Skills' },
    { label: 'Agents' },
    { label: 'Accounts' },
    { label: 'Launch' },
  ];

  // Esc closes the wizard. Operator request 2026-05-29.
  $effect(() => {
    function onKey(e) { if (e.key === 'Escape') onclose?.(); }
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  });
</script>

<div class="wiz">
  <header class="head">
    <h1>+ Spawn workspace</h1>
    <button class="x" onclick={() => onclose?.()} aria-label="close">×</button>
  </header>

  <WizardStepper steps={stepLabels} current={current} onnavigate={jumpTo} />

  <div class="body">
    {#if current === 1}
      <Stage1Shape bind:selectedId={templateId} onselect={selectTemplate} />
    {:else if current === 2}
      <Stage2Repo bind:selectedRepo={repo} />
    {:else if current === 3}
      <Stage3Skills
        template={template}
        bind:agents
        bind:teamName
        bind:skillOverrides
      />
    {:else if current === 4}
      <Stage4AgentTypes
        agents={agents}
        bind:agentTypeOverrides
        bind:agentModelOverrides
      />
    {:else if current === 5}
      <Stage4Accounts
        agents={agents}
        agentTypeOverrides={agentTypeOverrides}
        agentModelOverrides={agentModelOverrides}
        bind:typeAccounts
        bind:agentAccountOverrides
      />
    {:else if current === 6}
      <Stage5Launch
        selection={{ template, repo, members: agents, teamName, skillOverrides, agentTypeOverrides, agentModelOverrides, typeAccounts, agentAccountOverrides }}
        bind:saveAsRecipe
        bind:controller={launchController}
        onlaunch={launchedThenClose}
      />
    {/if}
  </div>

  <footer class="foot">
    <button type="button" class="back" onclick={back} disabled={current === 1 || launchController.launching}>← Back</button>
    <button type="button" class="cancel" onclick={() => onclose?.()}>Cancel</button>
    {#if current < 6}
      <button type="button" class="next" onclick={next} disabled={!canAdvance}>Next →</button>
    {:else}
      <!-- #252 — Launch CTA hoisted into outer footer, always visible regardless of stage body scroll. -->
      <button type="button" class="next launch" onclick={() => launchController.launch()} disabled={!launchController.canLaunch}>
        {launchController.label}
      </button>
    {/if}
  </footer>
</div>

<style>
  .wiz {
    background: #0a0a0a;
    color: #f5f5f5;
    border: 1px solid #2a2a2a;
    border-radius: 10px;
    overflow: hidden;
    width: 820px;
    max-width: 96vw;
    max-height: 92vh;
    display: flex; flex-direction: column;
  }
  .head { display: flex; align-items: center; padding: 0.65rem 1rem; border-bottom: 1px solid #2a2a2a; }
  .head h1 { flex: 1; font-size: 1rem; margin: 0; }
  .x { background: transparent; border: 0; color: #888; font-size: 1.2rem; cursor: pointer; padding: 0 0.4rem; }
  .x:hover { color: #e74c3c; }
  .body { flex: 1; overflow-y: auto; }
  .foot { display: flex; gap: 0.5rem; padding: 0.85rem 1rem; border-top: 1px solid #2a2a2a; }
  .back, .cancel, .next {
    padding: 0.5rem 1rem; border-radius: 6px; font-weight: 600; cursor: pointer; font: inherit; border: 0;
  }
  .back { background: transparent; color: #87ceeb; border: 1px solid #2a2a2a; }
  .back:disabled { opacity: 0.35; cursor: not-allowed; }
  .cancel { background: transparent; color: #888; }
  .next { background: #87ceeb; color: #0a0a0a; margin-left: auto; }
  .next:disabled { opacity: 0.35; cursor: not-allowed; }
</style>
