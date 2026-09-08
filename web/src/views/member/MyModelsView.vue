<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { apiMyModels } from '../../api/member'
import { fmtPrice, fmtQuota, pointsToYuan } from '../../utils/format'

const data = ref<any>(null)

onMounted(async () => {
  data.value = await apiMyModels()
})

const usedPct = computed(() => {
  if (!data.value || data.value.quota_limit == null) return null
  return (data.value.quota_used / data.value.quota_limit) * 100
})
</script>

<template>
  <div v-if="data" class="models">
    <!-- 我的额度：仪器读数 -->
    <el-card shadow="never" class="pool">
      <div class="pool-row">
        <div class="pool-item">
          <div class="pool-label">可用额度</div>
          <div class="pool-value num green">
            {{ fmtQuota(data.quota_limit == null ? 0 : data.quota_limit - data.quota_used) }}
          </div>
        </div>
        <div class="pool-item">
          <div class="pool-label">折合金额</div>
          <div class="pool-value num">
            {{ data.quota_limit == null ? '不限' : `¥${pointsToYuan(data.quota_limit - data.quota_used)}` }}
          </div>
        </div>
        <div class="pool-item">
          <div class="pool-label">已消耗</div>
          <div class="pool-value num">{{ fmtQuota(data.quota_used) }}</div>
        </div>
        <div v-if="usedPct != null" class="pool-meter-wrap">
          <div class="pool-meter" aria-hidden="true">
            <div class="pool-meter-fill" :style="{ width: `${Math.max(0.8, Math.min(100, usedPct))}%` }"></div>
          </div>
          <span class="pool-meter-label num">已用 {{ usedPct.toFixed(2) }}%</span>
        </div>
        <div class="pool-cta">
          <router-link to="/member/docs" class="docs-link">如何调用 →</router-link>
        </div>
      </div>
    </el-card>

    <el-card shadow="never">
      <template #header>我可用的大模型<span class="unit">（由客户管理员授权）</span></template>
      <el-table :data="data.models"
        empty-text="还没有被授权任何模型。请联系客户管理员在「子账号管理 → 模型授权」中为你勾选。">
        <el-table-column prop="name" label="模型名" min-width="160">
          <template #default="{ row }"><code>{{ row.name }}</code></template>
        </el-table-column>
        <el-table-column prop="display_name" label="说明" min-width="160" />
        <el-table-column prop="vendor" label="厂商" width="100" />
        <el-table-column label="输入单价" width="130" align="right">
          <template #default="{ row }"><span class="num">{{ fmtPrice(row.input_price) }}</span> <span class="dim">/百万token</span></template>
        </el-table-column>
        <el-table-column label="输出单价" width="130" align="right">
          <template #default="{ row }"><span class="num">{{ fmtPrice(row.output_price) }}</span> <span class="dim">/百万token</span></template>
        </el-table-column>
      </el-table>
      <p class="billing-note">
        计费说明：成本 = 输入 tokens × 输入单价 + 输出 tokens × 输出单价；1 元 = 1,000,000 token。
        调用方式见 <router-link to="/member/docs" class="docs-link">接入文档</router-link>。
      </p>
    </el-card>
  </div>
</template>

<style scoped>
.models { display: flex; flex-direction: column; gap: 16px; }
.unit { font-size: 12px; color: var(--tg-muted); font-weight: 400; margin-left: 4px; }
.dim { color: var(--tg-muted); font-size: 12px; }

.pool-row { display: flex; align-items: center; gap: 36px; flex-wrap: wrap; }
.pool-item { min-width: 110px; }
.pool-label { font-size: 12px; color: var(--tg-graphite); margin-bottom: 6px; }
.pool-value { font-size: 21px; font-weight: 600; font-variant-numeric: tabular-nums; }
.pool-value.green { color: var(--tg-green-ink); }
.pool-unit { font-size: 12px; color: var(--tg-muted); font-weight: 400; }

.pool-meter-wrap { flex: 1; min-width: 160px; display: flex; align-items: center; gap: 10px; }
.pool-meter { flex: 1; height: 8px; background: var(--tg-green-wash); border-radius: 4px; overflow: hidden; }
.pool-meter-fill { height: 100%; background: var(--tg-green); border-radius: 4px; }
.pool-meter-label { font-size: 11.5px; color: var(--tg-muted); white-space: nowrap; }

.pool-cta { margin-left: auto; }
.docs-link { color: var(--tg-green-ink); text-decoration: none; font-size: 13px; }
.docs-link:hover { text-decoration: underline; }

.billing-note { margin: 14px 0 0; font-size: 12.5px; color: var(--tg-muted); line-height: 1.8; }
</style>
