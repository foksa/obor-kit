<script lang="ts">
  // Note is an agent's pane note (what its status says as the terminal reports it: hc's blocked kind and msg, an
  // error's msg): one muted line cut with an ellipsis, its full text in the title; nothing when there is none. It is
  // text an agent wrote: always shown as text, never as markup.
  let { text = "" }: { text?: string } = $props();
</script>

{#if text}<span class="note muted small ell" title={text}>{text}</span>{/if}

<style>
  .note { display: inline-block; max-width: 100%; vertical-align: middle; }
</style>
