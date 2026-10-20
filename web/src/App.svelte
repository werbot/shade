<script lang="ts">
  import RuleForm, { blankRule } from "./RuleForm.svelte";
  import RuleList from "./RuleList.svelte";
  import TestPane from "./TestPane.svelte";
  import { listRules, type Candidate, type RuleList as Listing } from "./lib/api";

  let list = $state<Listing | null>(null); // null until the first answer
  let error = $state("");
  let draft = $state(blankRule());
  let patternError = $state("");
  // TestPane runs the test again when this changes: after a rule is created, switched or deleted.
  let refresh = $state(0);

  // While the form has a pattern, the test tries it alone; without one, the active rules.
  const candidate: Candidate | null = $derived(
    draft.pattern
      ? {
          type: draft.type,
          kind: draft.kind,
          pattern: draft.pattern,
          // A cleared number field binds null, and the server takes the group only as a number.
          secret_group: draft.secret_group ?? 0,
        }
      : null,
  );

  async function load() {
    try {
      list = await listRules();
    } catch (e) {
      error = `Cannot load the rules: ${(e as Error).message}`;
    }
  }

  function changed() {
    error = "";
    refresh++;
    load();
  }

  load();
</script>

<header>
  <h1>shade · Rules</h1>
  <div class="banner" role="alert" aria-live="assertive">{error}</div>
</header>

<main>
  <div class="form">
    {#if list}
      <RuleForm bind:draft types={list.types} project={list.project} {patternError} oncreated={changed} />
    {/if}
  </div>
  <section class="list" aria-labelledby="rules-title">
    <h2 id="rules-title">Rules</h2>
    {#if list}
      <RuleList rules={list.rules} onchange={changed} onerror={(message) => (error = message)} />
    {:else if !error}
      <p>Loading the rules…</p>
    {/if}
  </section>
  <div class="test">
    <TestPane {candidate} {refresh} onpatternerror={(message) => (patternError = message)} />
  </div>
</main>

<style>
  header,
  main {
    box-sizing: border-box;
    max-width: 80rem;
    margin: 0 auto;
    padding: var(--gap);
  }

  header {
    padding-bottom: 0;
  }

  .banner:not(:empty) {
    padding: var(--gap-s) var(--gap);
    border: 1px solid var(--danger);
    border-radius: var(--radius);
    color: var(--danger);
  }

  /* Wide: the form above the list on the left, the test on the right. Narrow: one column, with the
     test before the long list. */
  main {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    grid-template-rows: auto 1fr;
    grid-template-areas:
      "form test"
      "list test";
    gap: var(--gap) calc(2 * var(--gap));
  }

  .form {
    grid-area: form;
  }

  .list {
    grid-area: list;
  }

  .test {
    grid-area: test;
  }

  @media (max-width: 50rem) {
    main {
      grid-template-columns: minmax(0, 1fr);
      grid-template-rows: none;
      grid-template-areas: "form" "test" "list";
    }
  }
</style>
