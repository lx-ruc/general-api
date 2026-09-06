<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { apiGetOrg, apiOrgDetailStats } from '../../api/platform'
import LineChart from '../../components/LineChart.vue'
import { trendOptions, barOption } from '../../utils/chart'
import { fmtTime, fmtQuota, fmtNum, pointsToYuan, roleNames } from '../../utils/format'

const route = useRoute()
const router = useRouter()
const org = ref<any>(null)
const grants = ref<any[]>([])
const users = ref<any[]>([])
const stats = ref<any>(null)
const loading = ref(false)

onMounted(load)
async function load() {
  loading.value = true
  try {
    const id = Number(route.params.id)
    const resp = await apiGetOrg(id)
    org.value = resp.org
    grants.value = resp.quota_grants
    users.value = resp.users
    stats.value = await apiOrgDetailStats(id)
  } finally {
    loading.value = false
  }
}

const usedPct = computed(() =>
  org.value && org.value.quota_limit
    ? (org.value.quota_used / org.value.quota_limit) * 100 : 0,
)
</script>

<template>
  <div v-if="org" v-loading="loading" class="detail">
    <button class="back" type="button" @click="router.back()">
      <el-icon><ArrowLeft /></el-icon> 返回公司列表
    </button>
    <h1 class="org-name">
      {{ org.name }}
      <el-tag :type="org.status === 1 ? 'success' : 'danger'" effect="plain" size="small">
        {{ org.status === 1 ? '启用' : '停用' }}
      </el-tag>
    </h1>
    <p v-if="org.remark" class="org-remark">{{ org.remark }}</p>

    <!-- 额度读数 -->
    <el-card shadow="never" class="pool">
      <div class="pool-row">
        <div class="pool-item">
          <div class="pool-label">剩余额度</div>
          <div class="pool-value num green">
            {{ fmtQuota(org.quota_limit - org.quota_used) }}
          </div>
        </div>
        <div class="pool-item">
          <div class="pool-label">折合金额</div>
          <div class="pool-value num">¥{{ pointsToYuan(org.quota_limit - org.quota_used) }}</div>
        </div>
        <div class="pool-item">
          <div class="pool-label">额度上限</div>
          <div class="pool-value num">{{ fmtQuota(org.quota_limit) }}</div>
        </div>
        <div class="pool-item">
          <div class="pool-label">已消耗</div>
          <div class="pool-value num">{{ fmtQuota(org.quota_used) }}</div>
        </div>
        <div class="pool-meter-wrap">
          <div class="pool-meter" aria-hidden="true">
            <div class="pool-meter-fill" :style="{ width: `${Math.max(0.8, Math.min(100, usedPct))}%` }"></div>
          </div>
          <span class="pool-meter-label num">已用 {{ usedPct.toFixed(2) }}%</span>
        </div>
      </div>
    </el-card>

    <!-- 7 日趋势 -->
    <el-row v-if="stats" :gutter="16">
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>近 7 日请求</template>
          <LineChart :option="trendOptions(stats.series).reqOption" />
        </el-card>
      </el-col>
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>近 7 日成本<span class="unit">（token）</span></template>
          <LineChart :option="trendOptions(stats.series).costOption" />
        </el-card>
      </el-col>
    </el-row>

    <!-- 每个模型 / 每个员工的用量明细 -->
    <el-row v-if="stats" :gutter="16">
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>各模型用量<span class="unit">（按成本排序）</span></template>
          <LineChart v-if="stats.by_model.length"
            :option="barOption(stats.by_model.map((m: any) => m.name || '未路由'), stats.by_model.map((m: any) => m.cost))"
            height="200px" />
          <div class="chart-gap"></div>
          <el-table :data="stats.by_model" size="small"
            empty-text="该公司还没有调用记录。">
            <el-table-column prop="name" label="模型" min-width="140">
              <template #default="{ row }">
                <span v-if="row.name"><code>{{ row.name }}</code></span>
                <span v-else class="dim">（未路由）</span>
              </template>
            </el-table-column>
            <el-table-column prop="requests" label="请求数" width="80" align="right" />
            <el-table-column label="tokens" width="110" align="right">
              <template #default="{ row }"><span class="num">{{ fmtNum(row.tokens) }}</span></template>
            </el-table-column>
            <el-table-column label="成本" min-width="150" align="right">
              <template #default="{ row }">
                <span class="num green">{{ fmtQuota(row.cost) }}</span>
                <span class="dim"> · ¥{{ pointsToYuan(row.cost) }}</span>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>员工消耗<span class="unit">（按成本排序）</span></template>
          <LineChart v-if="stats.by_user.length"
            :option="barOption(stats.by_user.map((u: any) => u.name), stats.by_user.map((u: any) => u.cost))"
            height="200px" />
          <div class="chart-gap"></div>
          <el-table :data="stats.by_user" size="small"
            empty-text="暂无数据">
            <el-table-column prop="name" label="员工" min-width="110" />
            <el-table-column prop="requests" label="请求数" width="80" align="right" />
            <el-table-column label="tokens" width="110" align="right">
              <template #default="{ row }"><span class="num">{{ fmtNum(row.tokens) }}</span></template>
            </el-table-column>
            <el-table-column label="成本" min-width="150" align="right">
              <template #default="{ row }">
                <span class="num green">{{ fmtQuota(row.cost) }}</span>
                <span class="dim"> · ¥{{ pointsToYuan(row.cost) }}</span>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
    </el-row>

    <el-row :gutter="16">
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>额度流水<span class="unit">（最近 50 条）</span></template>
          <el-table :data="grants" size="small" max-height="420"
            empty-text="暂无流水。给公司追加额度后会记录在这里。">
            <el-table-column label="时间" width="160">
              <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
            </el-table-column>
            <el-table-column label="变更" width="130" align="right">
              <template #default="{ row }">
                <span class="num" :class="row.amount >= 0 ? 'green' : 'red'">
                  {{ row.amount >= 0 ? '+' : '' }}{{ fmtQuota(row.amount) }}
                </span>
              </template>
            </el-table-column>
            <el-table-column prop="remark" label="备注" show-overflow-tooltip />
          </el-table>
        </el-card>
      </el-col>
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>公司账号<span class="unit">（只读，日常管理由公司管理员进行）</span></template>
          <el-table :data="users" size="small" max-height="420" empty-text="暂无账号">
            <el-table-column prop="username" label="用户名" />
            <el-table-column prop="display_name" label="姓名" />
            <el-table-column label="角色" width="100">
              <template #default="{ row }">{{ roleNames[row.role] || row.role }}</template>
            </el-table-column>
            <el-table-column label="已用 / 上限" width="150" align="right">
              <template #default="{ row }">
                <span class="num">{{ fmtQuota(row.quota_used) }}</span>
                <span class="dim"> / </span>
                <span v-if="row.quota_limit == null" class="dim">不限</span>
                <span v-else class="num">{{ fmtQuota(row.quota_limit) }}</span>
              </template>
            </el-table-column>
            <el-table-column label="状态" width="70" align="center">
              <template #default="{ row }">
                <el-tag size="small" :type="row.status === 1 ? 'success' : 'danger'" effect="plain">
                  {{ row.status === 1 ? '启用' : '停用' }}
                </el-tag>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>

<style scoped>
.detail { display: flex; flex-direction: column; gap: 16px; }
.chart-gap { height: 12px; }
.unit { font-size: 12px; color: var(--tg-muted); font-weight: 400; margin-left: 4px; }
.dim { color: var(--tg-muted); font-size: 12px; }
.green { color: var(--tg-green-ink); }
.red { color: var(--tg-red); }

.back {
  align-self: flex-start; display: inline-flex; align-items: center; gap: 5px;
  background: none; border: none; cursor: pointer;
  color: var(--tg-graphite); font-size: 13px; padding: 0;
}
.back:hover { color: var(--tg-green-ink); }
.org-name { font-size: 20px; font-weight: 600; margin: -4px 0 0; display: flex; align-items: center; gap: 10px; }
.org-remark { margin: 0 0 -2px; font-size: 13px; color: var(--tg-muted); }

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
</style>
