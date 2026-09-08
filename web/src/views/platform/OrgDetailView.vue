<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import dayjs from 'dayjs'
import {
  apiGetOrg, apiUpdateOrg, apiOrgDetailStats, apiResetOrgAdminPassword, apiUpdateOrgAlertLevels,
  apiOrgStatement, downloadOrgStatementCSVPlatform,
} from '../../api/platform'
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

// 重置客户管理员密码（忘记密码时系统管理员的恢复入口）
const admins = computed(() => (users.value || []).filter((u: any) => u.role === 'org_admin'))
const resetVisible = ref(false)
const resetForm = ref({ userId: 0, new_password: '' })
function openReset() {
  resetForm.value = { userId: admins.value[0]?.id || 0, new_password: '' }
  resetVisible.value = true
}
async function submitReset() {
  if (!resetForm.value.new_password || resetForm.value.new_password.length < 6) {
    ElMessage.warning('新密码至少 6 位')
    return
  }
  const resp = await apiResetOrgAdminPassword(
    Number(route.params.id), resetForm.value.new_password, resetForm.value.userId || undefined)
  ElMessage.success(`已重置「${resp.username}」的密码，请立即告知对方`)
  resetVisible.value = false
}

const usedPct = computed(() =>
  org.value && org.value.quota_limit
    ? (org.value.quota_used / org.value.quota_limit) * 100 : 0,
)

// 额度预警阈值（alert_levels JSON 取最高档；0 = 关闭）
const threshold = ref(0)
const thresholdSaving = ref(false)
function parseThreshold(levels: string): number {
  try {
    const arr = JSON.parse(levels || '[]') as number[]
    return arr.length ? Math.max(...arr.filter((v) => v > 0), 0) : 0
  } catch {
    return 0
  }
}
onMounted(() => { threshold.value = parseThreshold(org.value?.alert_levels) })
watch(() => org.value?.alert_levels, (v) => { threshold.value = parseThreshold(v) })
async function saveThreshold() {
  thresholdSaving.value = true
  try {
    await apiUpdateOrgAlertLevels(Number(route.params.id), threshold.value || 0)
    ElMessage.success('预警阈值已更新')
  } finally {
    thresholdSaving.value = false
  }
}

// 单月消费上限（0=不限；当月达限拦截新请求，次月自动清零）
const monthly = ref(0)
const monthlyUsed = computed(() =>
  org.value && org.value.monthly_period === dayjs().format('YYYY-MM') ? (org.value.monthly_cost || 0) : 0,
)
onMounted(() => { monthly.value = org.value?.monthly_quota || 0 })
watch(() => org.value?.monthly_quota, (v) => { monthly.value = v || 0 })
async function saveMonthly() {
  monthlySaving.value = true
  try {
    await apiUpdateOrg(Number(route.params.id), { monthly_quota: monthly.value || 0 })
    ElMessage.success('单月上限已更新')
  } finally {
    monthlySaving.value = false
  }
}
const monthlySaving = ref(false)

// 三段式对账单（平台视角：明细含厂商成本/毛利）
const stMonth = ref(dayjs().format('YYYY-MM'))
const st = ref<any>(null)
const stLoading = ref(false)
const stMonthOptions = (() => {
  const out: string[] = []
  for (let i = 0; i < 12; i++) out.push(dayjs().subtract(i, 'month').format('YYYY-MM'))
  return out
})()
async function loadStatement() {
  stLoading.value = true
  try {
    st.value = await apiOrgStatement(Number(route.params.id), stMonth.value)
  } catch {
    st.value = null
  } finally {
    stLoading.value = false
  }
}
onMounted(loadStatement)
async function exportStatementCSV() {
  try {
    await downloadOrgStatementCSVPlatform(Number(route.params.id), stMonth.value, org.value?.name || '')
    ElMessage.success('账单 CSV 已生成')
  } catch {
    ElMessage.error('导出失败，请重试')
  }
}
</script>

<template>
  <div v-if="org" v-loading="loading" class="detail">
    <button class="back" type="button" @click="router.back()">
      <el-icon><ArrowLeft /></el-icon> 返回客户列表
    </button>
    <h1 class="org-name">
      {{ org.name }}
      <el-tag v-if="org.status === 1" type="success" effect="plain" size="small">启用</el-tag>
      <el-tag v-else-if="org.status === 2" type="danger" effect="dark" size="small">欠费停服</el-tag>
      <el-tag v-else type="danger" effect="plain" size="small">停用</el-tag>
    </h1>
    <p v-if="org.remark || org.contact_name || org.contact_phone" class="org-remark">
      <template v-if="org.remark">{{ org.remark }}<template v-if="org.contact_name"> · </template></template>
      <template v-if="org.contact_name">联系人：{{ org.contact_name }}</template>
      <template v-if="org.contact_phone"> {{ org.contact_phone }}</template>
    </p>

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
        <div class="pool-alert">
          <el-tooltip content="客户额度达到阈值时邮件提醒其管理员；0 = 关闭。达 100% 会同时通知你（系统管理员）" placement="top">
            <span class="pool-label">预警阈值</span>
          </el-tooltip>
          <div class="pool-alert-input">
            <el-input-number v-model="threshold" :min="0" :max="100" :controls="false" size="small" style="width: 76px" />
            <span class="dim">%</span>
            <el-button size="small" :loading="thresholdSaving" @click="saveThreshold">保存</el-button>
          </div>
        </div>
        <div class="pool-alert">
          <el-tooltip content="单月消费上限（token 预算）；0 = 不限。当月达限拦截新请求，次月自动清零恢复" placement="top">
            <span class="pool-label">单月上限<span v-if="monthly > 0" class="dim">（本月已用 ¥{{ pointsToYuan(monthlyUsed) }}）</span></span>
          </el-tooltip>
          <div class="pool-alert-input">
            <el-input-number v-model="monthly" :min="0" :step="10000000" size="small" style="width: 140px" />
            <el-button size="small" :loading="monthlySaving" @click="saveMonthly">保存</el-button>
          </div>
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

    <!-- 每个模型 / 每个子账号的用量明细 -->
    <el-row v-if="stats" :gutter="16">
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>各模型用量<span class="unit">（按成本排序）</span></template>
          <LineChart v-if="stats.by_model.length"
            :option="barOption(stats.by_model.map((m: any) => m.name || '未路由'), stats.by_model.map((m: any) => m.cost))"
            height="200px" />
          <div class="chart-gap"></div>
          <el-table :data="stats.by_model" size="small"
            empty-text="该客户还没有调用记录。">
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
          <template #header>子账号消耗<span class="unit">（按成本排序）</span></template>
          <LineChart v-if="stats.by_user.length"
            :option="barOption(stats.by_user.map((u: any) => u.name), stats.by_user.map((u: any) => u.cost))"
            height="200px" />
          <div class="chart-gap"></div>
          <el-table :data="stats.by_user" size="small"
            empty-text="暂无数据">
            <el-table-column prop="name" label="子账号" min-width="110" />
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
            empty-text="暂无流水。给客户追加额度后会记录在这里。">
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
          <template #header>
            <div class="card-head">
              <span>客户账号<span class="unit">（只读，日常管理由客户管理员进行）</span></span>
              <el-button size="small" @click="openReset">重置管理员密码</el-button>
            </div>
          </template>
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

    <!-- 三段式对账单（平台视角） -->
    <el-card v-if="st" shadow="never">
      <template #header>
        <div class="card-head">
          <span>月度对账单<span class="unit">（勾稽 / 冲减 / 明细，平台视角含厂商成本与毛利）</span></span>
          <div>
            <el-select v-model="stMonth" size="small" style="width: 120px; margin-right: 8px" @change="loadStatement">
              <el-option v-for="m in stMonthOptions" :key="m" :label="m" :value="m" />
            </el-select>
            <el-button type="primary" size="small" @click="exportStatementCSV">导出 CSV</el-button>
          </div>
        </div>
      </template>

      <el-descriptions :column="3" border size="small">
        <el-descriptions-item label="期初限额">
          <span class="num">{{ st.opening_limit == null ? '—' : fmtQuota(st.opening_limit) }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="期初已用">
          <span class="num">{{ st.opening_used == null ? '—' : fmtQuota(st.opening_used) }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="链式校验">
          <el-tag v-if="st.chain_ok === true" type="success" size="small">✓ 期末−期初 == 消耗</el-tag>
          <el-tag v-else-if="st.chain_ok === false" type="danger" size="small">✗ 数据不一致</el-tag>
          <el-tag v-else type="info" size="small">— 无期初快照</el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="期内授权">
          <span class="num green">+{{ fmtQuota(st.total_granted) }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="期内冲减">
          <span class="num red">{{ fmtQuota(st.total_revoked) }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="期内消耗（营收）">
          <span class="num">¥{{ pointsToYuan(st.consumption) }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="期末限额">
          <span class="num">{{ fmtQuota(st.closing_limit) }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="期末已用">
          <span class="num">
            {{ fmtQuota(st.closing_used) }}
            <el-tag v-if="st.closing_is_live" type="warning" size="small" effect="plain">实时</el-tag>
          </span>
        </el-descriptions-item>
        <el-descriptions-item label="不计量笔数">
          <span :class="{ 'st-warn': st.no_usage_count > 0 }">{{ st.no_usage_count }}</span>
        </el-descriptions-item>
      </el-descriptions>

      <el-table v-if="st.revokes && st.revokes.length" :data="st.revokes" size="small" class="st-revokes">
        <el-table-column label="冲减时间" width="160">
          <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
        </el-table-column>
        <el-table-column label="冲减金额" width="180" align="right">
          <template #default="{ row }">
            <span class="num red">{{ row.amount.toLocaleString('zh-CN') }} 点</span>
          </template>
        </el-table-column>
        <el-table-column prop="remark" label="事由" min-width="160" show-overflow-tooltip />
      </el-table>

      <h4 class="st-title">消耗明细<span class="unit">（模型 × 成本中心 × 日，未归集置底）</span></h4>
      <el-table :data="st.rows" size="small" empty-text="该月无消耗">
        <el-table-column prop="day" label="日期" width="100" />
        <el-table-column prop="model_name" label="模型" min-width="130" />
        <el-table-column label="成本中心" min-width="100">
          <template #default="{ row }">
            <span v-if="row.cost_center_id">{{ row.cost_center_name || `#${row.cost_center_id}` }}</span>
            <span v-else class="dim">未归集</span>
          </template>
        </el-table-column>
        <el-table-column prop="requests" label="请求" width="70" align="right" />
        <el-table-column label="缓存命中" width="80" align="right">
          <template #default="{ row }">
            <span :class="{ green: row.cache_hits > 0 }">{{ row.cache_hits }}</span>
          </template>
        </el-table-column>
        <el-table-column label="营收（点）" width="130" align="right">
          <template #default="{ row }">
            <span class="num green">{{ row.cost.toLocaleString('zh-CN') }}</span>
          </template>
        </el-table-column>
        <el-table-column label="厂商成本（点）" width="120" align="right">
          <template #default="{ row }">
            <span class="num">{{ (row.vendor_cost ?? 0).toLocaleString('zh-CN') }}</span>
          </template>
        </el-table-column>
        <el-table-column label="毛利（点）" width="110" align="right">
          <template #default="{ row }">
            <span class="num" :class="row.margin >= 0 ? 'green' : 'red'">
              {{ (row.margin ?? 0).toLocaleString('zh-CN') }}
            </span>
          </template>
        </el-table-column>
      </el-table>
      <div v-if="st.total_rows > st.rows.length" class="dim st-more">
        明细共 {{ st.total_rows }} 行，仅展示前 {{ st.rows.length }} 行；完整数据请导出 CSV。
      </div>
    </el-card>

    <!-- 重置管理员密码 -->
    <el-dialog v-model="resetVisible" title="重置客户管理员密码" width="420px">
      <p class="reset-hint">
        用于管理员忘记密码时的恢复。新密码只在此刻有效传递 — 平台侧不保存明文，
        请重置后立即告知对方，并提醒其登录后在「修改密码」中改成自己的密码。
      </p>
      <el-form label-width="80px">
        <el-form-item v-if="admins.length > 1" label="管理员">
          <el-select v-model="resetForm.userId" style="width: 100%">
            <el-option v-for="a in admins" :key="a.id" :label="`${a.username}（${a.display_name || a.username}）`" :value="a.id" />
          </el-select>
        </el-form-item>
        <el-form-item v-else label="管理员">{{ admins[0]?.username || '-' }}</el-form-item>
        <el-form-item label="新密码">
          <el-input v-model="resetForm.new_password" show-password placeholder="至少 6 位" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="resetVisible = false">取消</el-button>
        <el-button type="primary" @click="submitReset">重置</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.detail { display: flex; flex-direction: column; gap: 16px; }
.chart-gap { height: 12px; }
.card-head { display: flex; justify-content: space-between; align-items: center; }
.reset-hint { margin: 0 0 14px; font-size: 12.5px; color: var(--tg-graphite); line-height: 1.8; }
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
.pool-alert { display: flex; flex-direction: column; gap: 6px; }
.pool-alert-input { display: flex; align-items: center; gap: 6px; }

.st-revokes { margin-top: 14px; }
.st-title { margin: 18px 0 8px; font-size: 13.5px; }
.st-warn { color: var(--el-color-warning); font-weight: 600; }
.st-more { font-size: 12px; margin-top: 8px; }
</style>
