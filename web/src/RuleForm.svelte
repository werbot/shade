<script module lang="ts">
  import type { NewRule } from "./lib/api";

  /** The empty form: the draft at the start, and again once a rule is created. */
  export const blankRule = (): NewRule => ({
    name: "",
    type: "SECRET",
    kind: "regex",
    pattern: "",
    secret_group: 0,
    scope: "project",
  });
</script>

<script lang="ts">
  import { createRule } from "./lib/api";

  interface Props {
    draft: NewRule;
    types: string[];
    project: string; // the root the "project" scope means
    patternError: string; // what the test said of the pattern, "" when it compiles
    oncreated: () => void;
  }
  let { draft = $bindable(), types, project, patternError, oncreated }: Props = $props();

  let saving = $state(false);
  let failure = $state("");

  async function submit(e: SubmitEvent) {
    // fetch, not the native post: the server refuses the Origin: null that a form post would carry.
    e.preventDefault();
    saving = true;
    try {
      // A cleared number field binds null, and the server takes the group only as a number.
      await createRule({ ...draft, secret_group: draft.secret_group ?? 0 });
      draft = blankRule();
      failure = "";
      oncreated();
    } catch (err) {
      failure = (err as Error).message;
    } finally {
      saving = false;
    }
  }
</script>

<form onsubmit={submit} aria-labelledby="new-rule">
  <h2 id="new-rule">New rule</h2>

  <div class="field">
    <label for="rule-name">Name</label>
    <input id="rule-name" bind:value={draft.name} required autocomplete="off" spellcheck="false" />
  </div>

  <div class="pair">
    <div class="field">
      <label for="rule-type">Type</label>
      <select id="rule-type" bind:value={draft.type}>
        {#each types as type (type)}
          <option value={type}>{type}</option>
        {/each}
      </select>
    </div>
    <div class="field">
      <label for="rule-kind">Kind</label>
      <select id="rule-kind" bind:value={draft.kind}>
        <option value="regex">regex</option>
        <option value="literal">literal</option>
      </select>
    </div>
  </div>

  <div class="field">
    <label for="rule-pattern">Pattern</label>
    <input
      id="rule-pattern"
      class="mono"
      bind:value={draft.pattern}
      required
      autocomplete="off"
      spellcheck="false"
      aria-invalid={patternError !== ""}
      aria-describedby="rule-pattern-error"
    />
    <p id="rule-pattern-error" class="error" aria-live="polite">{patternError}</p>
  </div>

  <div class="field">
    <label for="rule-group">Secret group</label>
    <input id="rule-group" type="number" min="0" step="1" bind:value={draft.secret_group} />
  </div>

  <fieldset>
    <legend>Scope</legend>
    <label><input type="radio" name="scope" value="project" bind:group={draft.scope} /> this project: <code>{project}</code></label>
    <label><input type="radio" name="scope" value="global" bind:group={draft.scope} /> global: every project</label>
  </fieldset>

  <button type="submit" class="primary" disabled={saving}>Create rule</button>
  <p class="error" aria-live="polite">{failure}</p>
</form>

<style>
  form,
  fieldset {
    display: grid;
    gap: var(--gap-s);
  }

  .field {
    display: grid;
    gap: 0.2rem;
  }

  .pair {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
    gap: var(--gap-s);
  }

  .mono {
    font-family: var(--mono);
  }

  fieldset {
    margin: 0;
    padding: var(--gap-s);
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }

  .primary {
    justify-self: start;
    border-color: var(--accent);
    background: var(--accent);
    color: var(--accent-fg);
  }
</style>
