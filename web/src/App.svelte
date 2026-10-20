<script lang="ts">
  import RuleForm, { blankRule } from "./RuleForm.svelte";
  import RuleList from "./RuleList.svelte";
  import TestPane from "./TestPane.svelte";
  import { listRules, type Candidate, type RuleList as Listing } from "./lib/api";

  let list = $state<Listing | null>(null); // null until the first answer
  let error = $state(""); // the last failure of a load or of a change of the rules
  let testError = $state(""); // the last failure of the live test; its next run replaces it
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

  // Two quick changes start two loads, and the older answer may come last: each load takes a number,
  // and only the latest one applies what it got.
  let loads = 0;

  async function load() {
    const n = ++loads;
    try {
      const fresh = await listRules();
      if (n === loads) list = fresh;
    } catch (e) {
      if (n === loads) error = `Cannot load the rules: ${(e as Error).message}`;
    }
  }

  function changed() {
    error = "";
    refresh++;
    load();
  }

  // A refused change may come from a list that is out of date, so the server's list replaces it.
  function failed(message: string) {
    error = message;
    load();
  }

  load();
</script>

<header>
  <h1>shade · Rules</h1>
  <div class="banner" role="alert" aria-live="assertive">{error || testError}</div>
</header>

<!-- The source order is the narrow layout's order, so Tab and a screen reader follow what is seen. -->
<main>
  <div class="form">
    {#if list}
      <RuleForm bind:draft types={list.types} project={list.project} {patternError} oncreated={changed} />
    {/if}
  </div>
  <div class="test">
    <TestPane
      {candidate}
      {refresh}
      onpatternerror={(message) => (patternError = message)}
      onerror={(message) => (testError = message)}
    />
  </div>
  <section class="list" aria-labelledby="rules-title">
    <!-- tabindex="-1": RuleList puts the focus here when a deleted row takes its button away. -->
    <h2 id="rules-title" tabindex="-1">Rules</h2>
    {#if list}
      <RuleList rules={list.rules} onchange={changed} onerror={failed} />
    {:else if !error}
      <p>Loading the rules…</p>
    {/if}
  </section>
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
