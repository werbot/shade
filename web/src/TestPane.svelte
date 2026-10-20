<script lang="ts">
  import Highlight from "./Highlight.svelte";
  import { ApiError, testText, type Candidate, type TestResult } from "./lib/api";

  interface Props {
    candidate: Candidate | null; // the form's rule while it has a pattern
    refresh: number; // changes when the rules do
    onpatternerror: (message: string) => void;
    onerror: (message: string) => void; // a failure of the test itself, "" from a run without one
  }
  let { candidate, refresh, onpatternerror, onerror }: Props = $props();

  let text = $state("");
  // The last answer with the mode it was asked in, so the status never pairs it with another mode.
  let answer = $state<{ mode: string; result: TestResult | null; failure: string } | null>(null);

  const mode = $derived(candidate ? "Pattern from the form" : "All active rules");

  // The status speaks of what is shown: an answer stays until the next run replaces it.
  const status = $derived.by(() => {
    if (!answer) return `${mode}: ${text ? "testing…" : "type a text to test"}`;
    if (!answer.result) return `${answer.mode}: ${answer.failure}`;
    const n = answer.result.spans.length;
    return `${answer.mode}: ${n === 0 ? "no matches" : n === 1 ? "1 match" : `${n} matches`}`;
  });

  // run tests the text and shows the answer. Every run that is not cancelled tells the form what it
  // found of the pattern and App what failed, "" included, so an error never outlives the pattern or
  // the text it was about.
  async function run(body: string, rule: Candidate | null, asked: string, signal: AbortSignal) {
    let badPattern = false;
    let message = "";
    try {
      answer = body ? { mode: asked, result: await testText(body, rule, signal), failure: "" } : null;
    } catch (e) {
      if (signal.aborted) return; // a newer run took over
      // A pattern is broken half of the time while it is typed: the form shows why. Any other failure
      // is the test's own (no server, a text too large), and App's banner shows it.
      badPattern = rule !== null && e instanceof ApiError && e.status === 400;
      message = (e as Error).message;
      const failure = badPattern ? "see the error under the pattern" : "no answer, see the error above";
      answer = { mode: asked, result: null, failure };
    }
    onpatternerror(badPattern ? message : "");
    onerror(badPattern ? "" : message);
  }

  $effect(() => {
    const body = text, rule = candidate, asked = mode;
    void refresh; // read only, so that a change of the rules runs the test again
    const ctrl = new AbortController();
    const timer = setTimeout(() => run(body, rule, asked, ctrl.signal), 250);
    return () => {
      clearTimeout(timer);
      ctrl.abort();
    };
  });
</script>

<section aria-labelledby="test-title">
  <h2 id="test-title">Test</h2>
  <label for="test-text">Text</label>
  <textarea id="test-text" bind:value={text} rows="6" spellcheck="false"></textarea>
  <p role="status">{status}</p>
  {#if answer?.result}
    <Highlight text={answer.result.text} spans={answer.result.spans} />
  {/if}
</section>

<style>
  section {
    display: grid;
    gap: var(--gap-s);
  }

  textarea {
    font-family: var(--mono);
    resize: vertical;
  }

  p {
    margin: 0;
    color: var(--muted);
  }
</style>
