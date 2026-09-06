<script setup lang="ts">
defineProps<{
  title: string
  value: string
  sub?: string
  icon?: string
}>()
</script>

<template>
  <el-card shadow="never" class="stat-card">
    <div class="stat-body">
      <div class="stat-info">
        <div class="stat-title">{{ title }}</div>
        <div class="stat-value">{{ value }}</div>
        <div v-if="sub" class="stat-sub">{{ sub }}</div>
      </div>
      <el-icon v-if="icon" class="stat-icon"><component :is="icon" /></el-icon>
    </div>
  </el-card>
</template>

<style scoped>
.stat-body { display: flex; justify-content: space-between; align-items: center; }
.stat-title { font-size: 13px; color: #909399; margin-bottom: 8px; }
.stat-value { font-size: 24px; font-weight: 600; color: #303133; }
.stat-sub { font-size: 12px; color: #909399; margin-top: 6px; }
.stat-icon { font-size: 40px; color: #409eff; opacity: 0.25; }
</style>
