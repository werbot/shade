<script lang="ts">
  import { segments, type Span } from "./lib/segments";

  // text is the text of the answer, not of the input: the spans index the text the server read.
  let { text, spans }: { text: string; spans: Span[] } = $props();
</script>

<div class="out">
  {#each segments(text, spans) as seg, i (i)}
    {#if seg.span}
      <mark>{seg.text}<span class="rule">{seg.span.rule}</span></mark>
    {:else}
      {seg.text}
    {/if}
  {/each}
</div>

<style>
  .out {
    padding: var(--gap-s);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    font-family: var(--mono);
    /* The text keeps its line breaks, and a long run without spaces wraps inside the box. */
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }

  mark {
    border-radius: 3px;
    background: var(--mark);
    color: var(--mark-fg);
  }

  /* The name of the rule is the label of the match, so colour is not the only sign of it. As an
     inline block it moves to the next line whole, and breaks only when it is longer than a line. */
  .rule {
    display: inline-block;
    margin-inline-start: 0.25em;
    padding: 0 0.3em;
    border-radius: 3px;
    background: var(--fg);
    color: var(--bg);
    font-family: var(--sans);
    font-size: 0.75em;
  }
</style>
