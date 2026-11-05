<script setup lang="ts">
// 厂商标签组：优先展示承接渠道的厂商标签（vendors，与渠道管理页同源、可多组并列，
// 如 volces + volces/Thu），无渠道标签时回退 models.vendor 手工值
const props = defineProps<{
  vendors?: string[]
  vendor?: string
}>()

const tags = (): string[] => {
  if (props.vendors && props.vendors.length > 0) return props.vendors
  return props.vendor ? [props.vendor] : []
}
</script>

<template>
  <span class="vendor-tags">
    <el-tag v-for="v in tags()" :key="v" size="small" effect="plain" class="vtag">{{ v }}</el-tag>
    <span v-if="tags().length === 0" class="none">—</span>
  </span>
</template>

<style scoped>
.vendor-tags { display: inline-flex; align-items: center; gap: 4px; flex-wrap: wrap; }
.vtag { font-size: 11px; }
.none { color: var(--tg-muted); }
</style>
