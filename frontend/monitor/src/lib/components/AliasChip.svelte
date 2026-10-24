<script lang="ts">
	let { alias }: { alias: import('../types').Alias } = $props();
</script>

<a class="chip chip-{alias.kind}" href={alias.url} target="_blank" rel="noopener noreferrer" title={alias.url}>
	{alias.host}
</a>
