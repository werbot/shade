<script lang="ts">
  import { deleteRule, ruleKey, setEnabled, type Rule } from "./lib/api";

  interface Props {
    rules: Rule[];
    onchange: () => void; // a rule was switched or deleted
    onerror: (message: string) => void;
  }
  let { rules, onchange, onerror }: Props = $props();

  // The key of the row whose deletion waits for the second button, if any.
  let confirming = $state<string | null>(null);

  async function toggle(rule: Rule, box: HTMLInputElement) {
    try {
      await setEnabled(rule, box.checked);
      onchange();
    } catch (e) {
      // The store kept the rule as it was, so the switch goes back to say so.
      box.checked = rule.enabled;
      onerror((e as Error).message);
    }
  }

  async function remove(rule: Rule) {
    confirming = null;
    // The focused button went with the confirmation, so the heading of the list (in App) takes the
    // focus, whatever the server answers. No scroll: the row may be far below the heading.
    document.getElementById("rules-title")?.focus({ preventScroll: true });
    try {
      await deleteRule(rule);
      onchange();
    } catch (e) {
      onerror((e as Error).message);
    }
  }
</script>

{#if rules.length === 0}
  <p class="quiet">No rules yet.</p>
{:else}
  <ul>
    {#each rules as rule (ruleKey(rule))}
      {@const key = ruleKey(rule)}
      {@const who = `${rule.name} (${rule.scope})`}
      <li>
        <div class="head">
          <span class="name">{rule.name}</span>
          <span class="quiet">{rule.type} · {rule.scope} · {rule.kind}</span>
        </div>
        <code>{rule.pattern}</code>
        <div class="actions">
          <label>
            <input
              type="checkbox"
              checked={rule.enabled}
              aria-label="{who} enabled"
              onchange={(e) => toggle(rule, e.currentTarget)}
            />
            enabled
          </label>
          {#if rule.builtin}
            <button type="button" disabled aria-label="Delete {who}">Delete</button>
            <span class="quiet">builtin — disable instead</span>
          {:else}
            <!-- The first button keeps the focus while it turns into Cancel; the second one deletes. -->
            <button
              type="button"
              aria-label="{confirming === key ? 'Cancel deleting' : 'Delete'} {who}"
              onclick={() => (confirming = confirming === key ? null : key)}
            >
              {confirming === key ? "Cancel" : "Delete"}
            </button>
            {#if confirming === key}
              <button type="button" class="danger" aria-label="Confirm delete {who}" onclick={() => remove(rule)}>
                Confirm delete
              </button>
            {/if}
          {/if}
        </div>
      </li>
    {/each}
  </ul>
{/if}

<style>
  ul {
    display: grid;
    gap: var(--gap-s);
    margin: 0;
    padding: 0;
    list-style: none;
  }

  li {
    display: grid;
    gap: 0.25rem;
    padding: var(--gap-s);
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }

  .head,
  .actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.25rem var(--gap-s);
  }

  .name {
    font-weight: 600;
  }

  .quiet {
    margin: 0;
    color: var(--muted);
    font-size: 0.875rem;
  }

  code {
    font-size: 0.875rem;
  }

  .danger {
    border-color: var(--danger);
    background: var(--danger);
    color: var(--danger-fg);
  }
</style>
