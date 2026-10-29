<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import dayjs from 'dayjs'
import {
  apiGetOrg, apiUpdateOrg, apiOrgDetailStats, apiResetOrgAdminPassword, apiUpdateOrgAlertLevels,
  apiOrgStatement, downloadOrgStatementCSVPlatform,
  apiListOrgModelPricesByOrg, apiSetOrgModelPrice, apiDeleteOrgModelPrice,
  type OrgModelPriceByOrgRow,
} from '../../api/platform'
import LineChart from '../../components/LineChart.vue'
import { trendOptions, barOption } from '../../utils/chart'
import {
  fmtTime, fmtNum, fmtYuan, yuanToPoints, pointsToYuan, roleNames,
  yuan1KToPoints, pointsToYuan1K, yuan1MToPoints, pointsToYuan1M,
} from '../../utils/format'
import { usePpy } from '../../composables/usePpy'

const route = useRoute()
const router = useRouter()
const ppy = usePpy()
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

// 额度预警双阈值：使用率百分比（alert_levels JSON 取最高档；0=关）
// + 剩余额度金额档（alert_balance 点数；0=关）。两档正交，任一命中即邮件提醒
const threshold = ref(0)
const alertBalanceYuan = ref(0)
const thresholdSaving = ref(false)
function parseThreshold(levels: string): number {
  try {
    const arr = JSON.parse(levels || '[]') as number[]
    return arr.length ? Math.max(...arr.filter((v) => v > 0), 0) : 0
  } catch {
    return 0
  }
}
function syncAlertForm() {
  threshold.value = parseThreshold(org.value?.alert_levels)
  alertBalanceYuan.value = pointsToYuan(org.value?.alert_balance || 0, ppy.value)
}
onMounted(syncAlertForm)
watch(() => [org.value?.alert_levels, org.value?.alert_balance], syncAlertForm)
async function saveThreshold() {
  thresholdSaving.value = true
  try {
    await apiUpdateOrgAlertLevels(Number(route.params.id), threshold.value || 0,
      yuanToPoints(alertBalanceYuan.value || 0, ppy.value))
    ElMessage.success('预警设置已更新')
  } finally {
    thresholdSaving.value = false
  }
}

// 单月消费上限（0=不限；当月达限拦截新请求，次月自动清零）——按元录入，入库换算点数
const monthly = ref(0)
const monthlyUsed = computed(() =>
  org.value && org.value.monthly_period === dayjs().format('YYYY-MM') ? (org.value.monthly_cost || 0) : 0,
)
function syncMonthly() {
  monthly.value = pointsToYuan(org.value?.monthly_quota || 0, ppy.value)
}
onMounted(syncMonthly)
watch(() => org.value?.monthly_quota, syncMonthly)
async function saveMonthly() {
  monthlySaving.value = true
  try {
    await apiUpdateOrg(Number(route.params.id), { monthly_quota: yuanToPoints(monthly.value || 0, ppy.value) })
    ElMessage.success('单月上限已更新')
  } finally {
    monthlySaving.value = false
  }
}
const monthlySaving = ref(false)

// ---- 模型定价（该客户在各模型上的单独价——客户维度的管理入口）----
// 读走 GET /orgs/:id/model-prices；写复用模型维度的 PUT/DELETE /model-prices/:name
// （与模型列表定价弹窗同一数据源、同一审计口径，两边入口等价）
const orgPricing = ref<OrgModelPriceByOrgRow[]>([])
const pricingLoading = ref(false)
const pricingSearch = ref('')
const pricingSaving = ref(false)

// 定价录入单位（元/1K ↔ 元/1M；存储口径不变仍是点/百万token，仅录入/回显换算）
const unit = ref<'1k' | '1m'>('1k')
const unitLabel = computed(() => (unit.value === '1k' ? '1K' : '1M'))
const unitStep = computed(() => (unit.value === '1k' ? 0.001 : 0.1))
const unitPrecision = computed(() => (unit.value === '1k' ? 6 : 4))
function toDisplay(points: number): number {
  return unit.value === '1k'
    ? pointsToYuan1K(points, ppy.value)
    : pointsToYuan1M(points, ppy.value)
}
function fromDisplay(v: number): number {
  return unit.value === '1k'
    ? yuan1KToPoints(v, ppy.value)
    : yuan1MToPoints(v, ppy.value)
}
// 切换单位：草稿/快照同批 ×1000 或 ÷1000（快照同口径换算，避免浮点误差误判 dirty）
function onUnitChange() {
  const f = unit.value === '1k' ? (v: number) => v / 1000 : (v: number) => v * 1000
  const conv = (v: number) => Number((f(v || 0)).toFixed(10))
  for (const k of Object.keys(drafts)) {
    drafts[k] = { ...drafts[k], in: conv(drafts[k].in), out: conv(drafts[k].out), cache: conv(drafts[k].cache) }
    if (snapshots[k]) {
      snapshots[k] = { ...snapshots[k], in: conv(snapshots[k].in), out: conv(snapshots[k].out), cache: conv(snapshots[k].cache) }
    }
  }
}

// 客户价改为手动保存：编辑先落在 drafts，点「保存客户价」才逐模型提交；
// snapshots 是加载时的原始值（与 drafts 同单位同口径换算，避免浮点误差误判 dirty）
type PriceDraft = { in: number; out: number; cache: number }
const drafts = reactive<Record<string, PriceDraft>>({})
const snapshots = reactive<Record<string, PriceDraft>>({})

function seedDrafts() {
  for (const k of Object.keys(drafts)) delete drafts[k]
  for (const k of Object.keys(snapshots)) delete snapshots[k]
  for (const r of orgPricing.value) {
    const d = {
      in: toDisplay(r.input_price), out: toDisplay(r.output_price),
      cache: toDisplay(r.input_cache_hit_price),
    }
    drafts[r.model_name] = { ...d }
    snapshots[r.model_name] = { ...d }
  }
}

async function loadPricing() {
  pricingLoading.value = true
  try {
    orgPricing.value = await apiListOrgModelPricesByOrg(Number(route.params.id))
    seedDrafts()
  } finally {
    pricingLoading.value = false
  }
}
onMounted(loadPricing)

const filteredPricing = computed(() => {
  const q = pricingSearch.value.trim().toLowerCase()
  if (!q) return orgPricing.value
  return orgPricing.value.filter((r) =>
    r.model_name.toLowerCase().includes(q) || (r.display_name || '').toLowerCase().includes(q))
})

function isDirty(modelName: string): boolean {
  const d = drafts[modelName]
  const s = snapshots[modelName]
  return !!d && !!s && (d.in !== s.in || d.out !== s.out || d.cache !== s.cache)
}
const dirtyCount = computed(() => orgPricing.value.filter((r) => isDirty(r.model_name)).length)

// 逐模型提交有改动的行；input-number 清空是 null，单价按 0 处理（缓存价 0=同输入价）
async function savePricing() {
  if (dirtyCount.value === 0) return
  pricingSaving.value = true
  try {
    const orgId = Number(route.params.id)
    let n = 0
    for (const r of orgPricing.value) {
      if (!isDirty(r.model_name)) continue
      const d = drafts[r.model_name]
      await apiSetOrgModelPrice(r.model_name, {
        org_id: orgId,
        input_price: fromDisplay(d.in || 0),
        output_price: fromDisplay(d.out || 0),
        input_cache_hit_price: fromDisplay(d.cache || 0),
      })
      n++
    }
    ElMessage.success(`已保存 ${n} 个模型的客户价`)
    await loadPricing()
  } finally {
    pricingSaving.value = false
  }
}

async function resetModelPrice(row: OrgModelPriceByOrgRow) {
  await ElMessageBox.confirm(
    `恢复 ${row.display_name || row.model_name} 使用模型默认价？`, '恢复默认',
    { type: 'warning' },
  )
  await apiDeleteOrgModelPrice(row.model_name, Number(route.params.id))
  ElMessage.success('已恢复默认价')
  await loadPricing()
}

// 月度账单（平台视角）
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
            {{ fmtYuan(org.quota_limit - org.quota_used, ppy) }}
          </div>
        </div>
        <div class="pool-item">
          <div class="pool-label">额度上限</div>
          <div class="pool-value num">{{ fmtYuan(org.quota_limit, ppy) }}</div>
        </div>
        <div class="pool-item">
          <div class="pool-label">已消耗</div>
          <div class="pool-value num">{{ fmtYuan(org.quota_used, ppy) }}</div>
        </div>
        <div class="pool-meter-wrap">
          <div class="pool-meter" aria-hidden="true">
            <div class="pool-meter-fill" :style="{ width: `${Math.max(0.8, Math.min(100, usedPct))}%` }"></div>
          </div>
          <span class="pool-meter-label num">已用 {{ usedPct.toFixed(2) }}%</span>
        </div>
        <div class="pool-alert">
          <el-tooltip content="客户额度使用率超过该百分比时邮件提醒其管理员；0 = 关闭。达 100% 会同时通知你（系统管理员）" placement="top">
            <span class="pool-label">使用率预警</span>
          </el-tooltip>
          <div class="pool-alert-input">
            <el-input-number v-model="threshold" :min="0" :max="100" :controls="false" size="small" style="width: 76px" />
            <span class="dim">%</span>
          </div>
        </div>
        <div class="pool-alert">
          <el-tooltip content="客户剩余额度低于该金额（元）时邮件提醒其管理员；0 = 关闭。与使用率预警相互独立，任一命中即提醒" placement="top">
            <span class="pool-label">余额预警</span>
          </el-tooltip>
          <div class="pool-alert-input">
            <el-input-number v-model="alertBalanceYuan" :min="0" :precision="2" :controls="false" size="small" style="width: 110px" />
            <span class="dim">元</span>
          </div>
        </div>
        <div class="pool-alert">
          <el-button size="small" :loading="thresholdSaving" @click="saveThreshold">保存预警</el-button>
        </div>
        <div class="pool-alert">
          <el-tooltip content="单月消费上限（元）；0 = 不限。当月达限拦截新请求，次月自动清零恢复" placement="top">
            <span class="pool-label">单月上限<span v-if="monthly > 0" class="dim">（本月已用 {{ fmtYuan(monthlyUsed, ppy) }}）</span></span>
          </el-tooltip>
          <div class="pool-alert-input">
            <el-input-number v-model="monthly" :min="0" :precision="2" :step="100" size="small" style="width: 140px" />
            <span class="dim">元</span>
            <el-button size="small" :loading="monthlySaving" @click="saveMonthly">保存</el-button>
          </div>
        </div>
      </div>
    </el-card>

    <!-- 模型定价（该客户在各模型上的单独价） -->
    <el-card shadow="never">
      <template #header>
        <div class="card-head">
          <span>模型定价<span class="unit">（未单独设价的模型按全局默认价计费）</span></span>
          <el-radio-group v-model="unit" size="small" @change="onUnitChange">
            <el-radio-button value="1k">元/1K</el-radio-button>
            <el-radio-button value="1m">元/1M</el-radio-button>
          </el-radio-group>
        </div>
      </template>
      <div class="pricing-toolbar">
        <el-input v-model="pricingSearch" placeholder="按模型名搜索" size="small" clearable style="width: 220px" />
        <span class="dim">缓存命中价 0 = 同输入价；停用模型仅在有单独价时列出</span>
      </div>
      <el-table :data="filteredPricing" v-loading="pricingLoading" size="small" max-height="420"
        empty-text="暂无模型（先在渠道接入模型）">
        <el-table-column label="模型" min-width="210">
          <template #default="{ row: r }">
            <div class="model-cell" :class="{ 'model-off': r.model_status === 0 }">
              <span>{{ r.display_name || r.model_name }}</span>
              <el-tag v-if="r.override" size="small" type="warning" effect="plain">单独价</el-tag>
              <el-tag v-else size="small" type="info" effect="plain">默认</el-tag>
              <el-tag v-if="r.model_status === 0" size="small" type="danger" effect="plain">停用</el-tag>
              <el-tag v-if="isDirty(r.model_name)" size="small" type="warning" effect="plain">未保存</el-tag>
            </div>
            <div v-if="r.display_name && r.display_name !== r.model_name" class="dim">{{ r.model_name }}</div>
          </template>
        </el-table-column>
        <el-table-column width="200" align="center">
          <template #header>
            <div class="col-head">默认价<span class="col-unit">输入 / 输出（元/{{ unitLabel }} token）</span></div>
          </template>
          <template #default="{ row: r }">
            <span class="dim">{{ toDisplay(r.def_input_price) }} / {{ toDisplay(r.def_output_price) }}</span>
          </template>
        </el-table-column>
        <el-table-column width="150" align="center">
          <template #header>
            <div class="col-head">客户价<span class="col-unit">输入（元/{{ unitLabel }}）</span></div>
          </template>
          <template #default="{ row: r }">
            <el-input-number v-model="drafts[r.model_name].in" :min="0" :step="unitStep" :precision="unitPrecision"
              size="small" style="width: 120px" :controls="false" />
          </template>
        </el-table-column>
        <el-table-column width="150" align="center">
          <template #header>
            <div class="col-head">客户价<span class="col-unit">输出（元/{{ unitLabel }}）</span></div>
          </template>
          <template #default="{ row: r }">
            <el-input-number v-model="drafts[r.model_name].out" :min="0" :step="unitStep" :precision="unitPrecision"
              size="small" style="width: 120px" :controls="false" />
          </template>
        </el-table-column>
        <el-table-column width="150" align="center">
          <template #header>
            <div class="col-head">客户价<span class="col-unit">缓存命中（元/{{ unitLabel }}）</span></div>
          </template>
          <template #default="{ row: r }">
            <el-input-number v-model="drafts[r.model_name].cache" :min="0" :step="unitStep" :precision="unitPrecision"
              size="small" style="width: 120px" :controls="false" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="110" align="center">
          <template #default="{ row: r }">
            <el-button v-if="r.override" size="small" @click="resetModelPrice(r)">恢复默认</el-button>
          </template>
        </el-table-column>
      </el-table>
      <div class="pricing-actions">
        <span v-if="dirtyCount > 0" class="dim">{{ dirtyCount }} 个模型未保存</span>
        <el-button type="primary" size="small" :loading="pricingSaving" :disabled="dirtyCount === 0" @click="savePricing">
          保存客户价
        </el-button>
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
          <template #header>近 7 日额度消耗<span class="unit">（元）</span></template>
          <LineChart :option="trendOptions(stats.series).costOption" />
        </el-card>
      </el-col>
    </el-row>

    <!-- 每个模型 / 每个子账号的用量明细 -->
    <el-row v-if="stats" :gutter="16">
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>各模型用量<span class="unit">（按消耗排序）</span></template>
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
            <el-table-column label="消耗" min-width="150" align="right">
              <template #default="{ row }">
                <span class="num green">{{ fmtYuan(row.cost, ppy) }}</span>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>子账号消耗<span class="unit">（按消耗排序）</span></template>
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
            <el-table-column label="消耗" min-width="150" align="right">
              <template #default="{ row }">
                <span class="num green">{{ fmtYuan(row.cost, ppy) }}</span>
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
                  {{ row.amount >= 0 ? '+' : '' }}{{ fmtYuan(row.amount, ppy) }}
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
                <span class="num">{{ fmtYuan(row.quota_used, ppy) }}</span>
                <span class="dim"> / </span>
                <span v-if="row.quota_limit == null" class="dim">不限</span>
                <span v-else class="num">{{ fmtYuan(row.quota_limit, ppy) }}</span>
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

    <!-- 月度账单（平台视角） -->
    <el-card v-if="st" shadow="never">
      <template #header>
        <div class="card-head">
          <span>月度账单<span class="unit">（{{ stMonth }} · 平台视角）</span></span>
          <div>
            <el-select v-model="stMonth" size="small" style="width: 120px; margin-right: 8px" @change="loadStatement">
              <el-option v-for="m in stMonthOptions" :key="m" :label="m" :value="m" />
            </el-select>
            <el-button type="primary" size="small" @click="exportStatementCSV">导出 CSV</el-button>
          </div>
        </div>
      </template>

      <el-descriptions :column="3" border size="small">
        <el-descriptions-item label="月初余额（元）">
          <span class="num">{{ st.opening_limit == null ? '—' : fmtYuan(st.opening_limit - (st.opening_used || 0), ppy) }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="授权（元）">
          <span class="num green">+{{ fmtYuan(st.total_granted, ppy) }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="回收（元）">
          <span class="num red">{{ fmtYuan(st.total_revoked, ppy) }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="消耗（元）">
          <span class="num">{{ fmtYuan(st.consumption, ppy) }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="月末余额（元）">
          <span class="num">{{ fmtYuan(st.closing_limit - st.closing_used, ppy) }}
            <el-tag v-if="st.closing_is_live" type="warning" size="small" effect="plain">当前</el-tag>
          </span>
        </el-descriptions-item>
        <el-descriptions-item label="账单费用（元）">
          <span class="num">{{ fmtYuan(st.consumption) }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="数据校验" :span="2">
          <el-tag v-if="st.chain_ok === true" type="success" size="small">✓ 月初余额 + 授权 − 消耗 == 月末余额</el-tag>
          <el-tag v-else-if="st.chain_ok === false" type="danger" size="small">✗ 数据不一致</el-tag>
          <el-tag v-else type="info" size="small">— 无月初数据，暂无法校验</el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="未计费请求">
          <span :class="{ 'st-warn': st.no_usage_count > 0 }">{{ st.no_usage_count }}</span>
        </el-descriptions-item>
      </el-descriptions>

      <el-table v-if="st.revokes && st.revokes.length" :data="st.revokes" size="small" class="st-revokes">
        <el-table-column label="回收时间" width="160">
          <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
        </el-table-column>
        <el-table-column label="回收（元）" width="180" align="right">
          <template #default="{ row }">
            <span class="num red">{{ fmtYuan(row.amount, ppy) }}</span>
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
        <el-table-column label="消耗（元）" width="130" align="right">
          <template #default="{ row }">
            <span class="num green">{{ fmtYuan(row.cost, ppy) }}</span>
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

.pricing-toolbar { display: flex; align-items: center; gap: 12px; margin-bottom: 10px; flex-wrap: wrap; }
.pricing-actions { display: flex; justify-content: flex-end; align-items: center; gap: 10px; margin-top: 12px; }
.model-cell { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.model-off { opacity: 0.55; }
.col-head { display: flex; flex-direction: column; line-height: 1.4; font-weight: 600; }
.col-unit { font-size: 11px; font-weight: 400; color: var(--tg-muted); white-space: nowrap; }
</style>
