<!--
  SpawnWizardV9 — 5-stage v0.9 wizard (operator restructure 2026-05-29).

  Stages:
    1 Shape    — Stage1Shape    template gallery
    2 Repo     — Stage2Repo     where the code lives
    3 Skills   — Stage3Skills   per-team role × skill matrix override
    4 Accounts — Stage4Accounts per-agent-type vault picker + Connect-Claude
    5 Launch   — Stage5Launch   spawn

  State threaded between stages:
    templateId / template / repo / teamName  (1+2)
    agents     — the slot roster (label/role_id) carried through
    skillOverrides[roleID] → string[] of skill IDs (set in Stage 3)
                  empty for a role = use that role's global default_skills
    typeAccounts[agentType] → vault entry id  (set in Stage 4)
    agentAccountOverrides[agentLabel] → vault entry id  (Stage 4 advanced)

  Hard rules:
    - Per-team override semantics: Stage 3 edits NEVER mutate the global
      role catalog. Only the dashboard 🎮 roles widget does that.
    - Launch is blocked until every agent has a resolvable account
      (typeAccounts[a.agent_type] OR agentAccountOverrides[a.label]).

  Props:
    onclose(): close handler from Workspace
-->
<script>
  import WizardStepper from './WizardStepper.svelte';
  import Stage1Shape from './Stage1Shape.svelte';
  import Stage2Repo from './Stage2Repo.svelte';
  import Stage3Skills from './Stage3Skills.svelte';
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

  // Stage 4 output. typeAccounts is the per-agent-type default;
  // agentAccountOverrides is the per-individual-agent override
  // (advanced mode in Stage 4).
  let typeAccounts = $state({});
  let agentAccountOverrides = $state({});

  function next() { if (current < 5) current += 1; }
  function back() { if (current > 1) current -= 1; }
  function jumpTo(step) { if (step >= 1 && step < current) current = step; }

  function accountFor(a) {
    return agentAccountOverrides[a.label] || typeAccounts[a.agent_type || 'claude-code'] || '';
  }

  const allAgentsHaveAccount = $derived.by(() =>
    agents.length > 0 && agents.every(a => !!accountFor(a))
  );

  const canAdvance = $derived.by(() => {
    switch (current) {
      case 1: return !!templateId;
      case 2: return !!repo;
      case 3: return agents.length > 0 && !!teamName;
      case 4: return allAgentsHaveAccount;  // block Launch until all accounts set
      case 5: return true;
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

  const stepLabels = [
    { label: 'Shape' },
    { label: 'Repo' },
    { label: 'Skills' },
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
      <Stage4Accounts
        agents={agents}
        bind:typeAccounts
        bind:agentAccountOverrides
      />
    {:else if current === 5}
      <Stage5Launch
        selection={{ template, repo, members: agents, teamName, skillOverrides, typeAccounts, agentAccountOverrides }}
        bind:saveAsRecipe
        onlaunch={launchedThenClose}
      />
    {/if}
  </div>

  <footer class="foot">
    <button type="button" class="back" onclick={back} disabled={current === 1}>← Back</button>
    <button type="button" class="cancel" onclick={() => onclose?.()}>Cancel</button>
    {#if current < 5}
      <button type="button" class="next" onclick={next} disabled={!canAdvance}>Next →</button>
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
