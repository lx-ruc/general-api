<script setup lang="ts">
export interface StatItem {
  label: string
  value: string | number
  unit?: string
  sub?: string
  tone?: 'default' | 'green' | 'danger'
}

defineProps<{ items: StatItem[] }>()
</script>

<template>
  <div class="stat-row">
    <div v-for="(it, i) in items" :key="i" class="stat" :class="{ divider: i > 0 }">
      <div class="stat-label">{{ it.label }}</div>
      <div class="stat-value" :class="it.tone">
        <span class="num">{{ it.value }}</span>
        <span v-if="it.unit" class="stat-unit">{{ it.unit }}</span>
      </div>
      <div v-if="it.sub" class="stat-sub">{{ it.sub }}</div>
    </div>
  </div>
</template>

<style scoped>
.stat-row {
  display: flex;
  background: var(--tg-surface);
  border: 1px solid var(--tg-line);
  border-radius: 8px;
  padding: 18px 6px;
}
.stat { flex: 1; padding: 2px 20px; min-width: 0; }
.stat.divider { border-left: 1px solid var(--tg-line); }

.stat-label { font-size: 12px; color: var(--tg-graphite); margin-bottom: 8px; }
.stat-value { display: flex; align-items: baseline; gap: 6px; }
.stat-value .num {
  font-size: 26px; font-weight: 600; color: var(--tg-ink);
  font-variant-numeric: tabular-nums; line-height: 1.1;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.stat-value.green .num { color: var(--tg-green-ink); }
.stat-value.danger .num { color: var(--tg-red); }
.stat-unit { font-size: 12px; color: var(--tg-muted); }
.stat-sub { font-size: 12px; color: var(--tg-muted); margin-top: 7px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

@media (max-width: 900px) {
  .stat-row { flex-wrap: wrap; }
  .stat { flex: 1 1 45%; }
  .stat:nth-child(3) { border-left: none; }
  .stat { margin-bottom: 12px; }
}
</style>
