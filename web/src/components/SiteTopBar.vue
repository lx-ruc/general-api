<script setup lang="ts">
// 登录/注册页顶栏：与管理台 AdminLayout 同款品牌条（绿点 + general api + 在线脉冲）
</script>

<template>
  <header class="site-top">
    <div class="site-brand">
      <span class="brand-dot" aria-hidden="true"></span>
      <span class="brand-name">general api</span>
    </div>
    <div class="site-meter" aria-hidden="true">
      <span class="meter-dot"></span>
      <span class="meter-label num">gateway online</span>
    </div>
  </header>
</template>

<style scoped>
.site-top {
  height: 60px; flex: none;
  display: flex; align-items: center; justify-content: space-between;
  padding: 0 24px;
  background: var(--tg-surface);
  border-bottom: 1px solid var(--tg-line);
}
.site-brand { display: flex; align-items: center; gap: 9px; }
.brand-dot {
  width: 9px; height: 9px; border-radius: 50%;
  background: var(--tg-green);
  box-shadow: 0 0 0 3px rgba(18, 164, 98, 0.16);
}
.brand-name {
  font-size: 15px; font-weight: 700; letter-spacing: 0.01em;
  color: var(--tg-ink);
  font-family: ui-monospace, 'SF Mono', Menlo, monospace;
}

.site-meter { display: flex; align-items: center; gap: 7px; }
.meter-dot {
  width: 6px; height: 6px; border-radius: 50%;
  background: var(--tg-green);
  animation: site-pulse 2.4s ease-in-out infinite;
}
.meter-label { font-size: 10.5px; color: var(--tg-muted); letter-spacing: 0.06em; }
@keyframes site-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.35; }
}
@media (prefers-reduced-motion: reduce) {
  .meter-dot { animation: none; }
}
</style>
