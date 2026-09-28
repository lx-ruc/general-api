<script setup lang="ts">
import { onMounted, ref } from 'vue'
import dayjs from 'dayjs'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useAuthStore } from '../../stores/auth'
import {
  apiOrgBilling, apiGetAlertLevels, apiUpdateAlertLevels,
  apiOrgBillingStatement, downloadOrgStatementCSV, type BillStatement,
} from '../../api/org'
import StatRow from '../../components/StatRow.vue'
import { fmtNum, fmtQuota, fmtYuan } from '../../utils/format'

const auth = useAuthStore()

const month = ref(dayjs().format('YYYY-MM'))
const data = ref<any>(null)
const st = ref<BillStatement | null>(null)
const loading = ref(false)

const monthOptions = (() => {
  const out: string[] = []
  for (let i = 0; i < 12; i++) out.push(dayjs().subtract(i, 'month').format('YYYY-MM'))
  return out
})()

async function load() {
  loading.value = true
  try {
    data.value = await apiOrgBilling(month.value)
  } finally {
    loading.value = false
  }
  loadStatement()
}
onMounted(load)

// 三段式账单（勾稽/冲减/明细）
async function loadStatement() {
  try {
    st.value = await apiOrgBillingStatement(month.value)
  } catch {
    st.value = null
  }
}

async function exportStatementCSV() {
  try {
    await downloadOrgStatementCSV(month.value, auth.user?.org_name || '本客户')
    ElMessage.success('账单 CSV 已生成')
  } catch {
    ElMessage.error('导出失败，请重试')
  }
}

// 额度预警阈值（0/空 = 关闭；升档邮件一次性提醒，拨备回落自动复位）
const threshold = ref<number>(80)
const thresholdLoading = ref(false)
const monthly = ref<{ quota: number; used: number }>({ quota: 0, used: 0 })
onMounted(async () => {
  try {
    const r = await apiGetAlertLevels()
    threshold.value = r.threshold
    monthly.value = { quota: r.monthly_quota || 0, used: r.monthly_used || 0 }
  } catch { /* 默认 80 */ }
})
async function saveThreshold() {
  thresholdLoading.value = true
  try {
    await apiUpdateAlertLevels(threshold.value || 0)
    ElMessageBox.alert('预警阈值已更新', '成功')
  } finally {
    thresholdLoading.value = false
  }
}

function exportCSV() {
  const d = data.value
  if (!d) return
  const lines = [
    `月份,${d.month}`,
    '汇总,请求数,tokens,消耗',
    `合计,${d.summary.requests},${d.summary.tokens},${d.summary.cost}`,
    '',
    '模型,请求数,tokens,消耗',
    ...d.by_model.map((m: any) => `${m.name},${m.requests},${m.tokens},${m.cost}`),
    '',
    '充值时间,金额（token）,状态,凭证说明',
    ...d.recharges.map((r: any) =>
      `${dayjs.unix(r.created_at).format('YYYY-MM-DD HH:mm')},${r.amount},${r.status},${(r.voucher || '').replace(/[,\n]/g, ' ')}`),
  ]
  const blob = new Blob(['﻿' + lines.join('\n')], { type: 'text/csv;charset=utf-8' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = `对账单_${d.month}.csv`
  a.click()
  URL.revokeObjectURL(a.href)
}
</script>

<template>
  <div v-loading="loading" class="wrap">
    <el-card shadow="never">
      <template #header>
        <div class="card-header">
          <span>月度对账单</span>
          <div>
            <el-select v-model="month" style="width: 130px; margin-right: 8px" @change="load">
              <el-option v-for="m in monthOptions" :key="m" :label="m" :value="m" />
            </el-select>
            <el-button @click="exportCSV">导出汇总 CSV</el-button>
          </div>
        </div>
      </template>
      <StatRow v-if="data" :items="[
        { label: '请求数（本月）', value: fmtNum(data.summary.requests) },
        { label: '消耗token（本月）', value: fmtQuota(data.summary.cost) },
        { label: '授权token（本月充值）', value: fmtQuota(data.recharged), tone: 'green' },
        { label: '账单费用（元）', value: fmtYuan(data.summary.cost) },
      ]" />
    </el-card>

    <el-card v-if="data" shadow="never">
      <template #header>额度预警</template>
      <div v-if="monthly.quota > 0" class="monthly-line">
        <span>单月消费上限：<b class="num">{{ fmtQuota(monthly.quota) }}</b>，
          本月已用 <b class="num">{{ fmtQuota(monthly.used) }}</b><el-progress
            :percentage="Math.min(100, monthly.quota ? (monthly.used / monthly.quota) * 100 : 0)"
            :stroke-width="8" :show-text="false" style="width: 180px; display: inline-block; margin-left: 10px; vertical-align: middle" />
        </span>
        <span class="dim tip-inline">达限后当月停用，次月自动清零恢复</span>
      </div>
      <div class="alert-cfg">
        <span>客户额度使用率达到阈值时邮件提醒客户管理员（每档只提醒一次，追加额度后自动复位）</span>
        <div class="alert-input">
          <el-input-number v-model="threshold" :min="0" :max="100" :controls="false" style="width: 90px" />
          <span class="dim">%</span>
          <el-button type="primary" size="small" :loading="thresholdLoading" @click="saveThreshold">保存</el-button>
        </div>
        <span class="dim tip">0 = 关闭预警；达 100%（额度耗尽）时同时通知系统管理员跟进续费</span>
      </div>
    </el-card>

    <el-card v-if="st" shadow="never">
      <template #header>
        <div class="card-header">
          <span>月度账单<span class="dim">（{{ st.month }}）</span></span>
          <el-button type="primary" size="small" @click="exportStatementCSV">导出账单 CSV</el-button>
        </div>
      </template>

      <!-- 余额对账：月初余额 + 授权 − 消耗 = 月末余额，校验数据自洽 -->
      <el-descriptions :column="3" border size="small" class="chain">
        <el-descriptions-item label="月初token余额">
          <span class="num">{{ st.opening_limit == null ? '—' : fmtQuota(st.opening_limit - (st.opening_used || 0)) }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="授权token">
          <span class="num green">+{{ fmtQuota(st.total_granted) }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="回收token">
          <span class="num red">{{ fmtQuota(st.total_revoked) }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="消耗token">
          <span class="num">{{ fmtQuota(st.consumption) }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="月末token余额">
          <span class="num">{{ fmtQuota(st.closing_limit - st.closing_used) }}
            <el-tag v-if="st.closing_is_live" type="warning" size="small" effect="plain">当前</el-tag>
          </span>
        </el-descriptions-item>
        <el-descriptions-item label="账单费用（元）">
          <span class="num">{{ fmtYuan(st.consumption) }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="数据校验" :span="2">
          <el-tag v-if="st.chain_ok === true" type="success" size="small">✓ 月初余额 + 授权 − 消耗 == 月末余额</el-tag>
          <el-tag v-else-if="st.chain_ok === false" type="danger" size="small">✗ 数据不一致，请联系平台</el-tag>
          <el-tag v-else type="info" size="small">— 无月初数据，暂无法校验</el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="未计费请求">
          <span :class="{ warn: st.no_usage_count > 0 }">{{ st.no_usage_count }}</span>
          <span v-if="st.no_usage_count > 0" class="dim">（上游未返回用量，未计费）</span>
        </el-descriptions-item>
      </el-descriptions>
      <div v-if="st.opening_missing" class="dim tip">
        更早的月份没有月初数据，暂无法自动校验；平台启用快照后的月份自动补全。
      </div>

      <!-- 回收段 -->
      <h4 class="sec-title">回收记录<span class="dim">（回收即负数授权，从额度里扣回）</span></h4>
      <el-table :data="st.revokes" size="small" empty-text="本月无回收">
        <el-table-column label="时间" width="150">
          <template #default="{ row }">{{ dayjs.unix(row.created_at).format('MM-DD HH:mm') }}</template>
        </el-table-column>
        <el-table-column label="金额（token）" width="180" align="right">
          <template #default="{ row }">
            <span class="num red">{{ fmtQuota(row.amount) }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="remark" label="事由" min-width="160" show-overflow-tooltip />
      </el-table>

      <!-- 明细段：模型 × 成本中心 × 日 -->
      <h4 class="sec-title">消耗明细<span class="dim">（按天 / 模型；缓存命中不扣额度）</span></h4>
      <el-table :data="st.rows" size="small" empty-text="本月无消耗">
        <el-table-column prop="day" label="日期" width="100" />
        <el-table-column prop="model_name" label="模型" min-width="130" />
        <el-table-column label="成本中心" min-width="110">
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
        <el-table-column label="tokens" width="120" align="right">
          <template #default="{ row }">
            <span class="dim">入 {{ fmtNum(row.prompt_tokens) }} / 出 {{ fmtNum(row.completion_tokens) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="消耗（token）" width="150" align="right">
          <template #default="{ row }">
            <span class="num">{{ fmtQuota(row.cost) }}</span>
          </template>
        </el-table-column>
      </el-table>
      <div v-if="st.total_rows > st.rows.length" class="dim tip">
        明细共 {{ st.total_rows }} 行，仅展示前 {{ st.rows.length }} 行；完整数据请导出 CSV。
      </div>
    </el-card>

    <el-row v-if="data" :gutter="16">
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>按模型明细</template>
          <el-table :data="data.by_model" size="small" empty-text="本月无调用">
            <el-table-column prop="name" label="模型" min-width="140" />
            <el-table-column prop="requests" label="请求数" width="80" align="right" />
            <el-table-column label="tokens" width="110" align="right">
              <template #default="{ row }"><span class="num">{{ fmtNum(row.tokens) }}</span></template>
            </el-table-column>
            <el-table-column label="消耗" min-width="150" align="right">
              <template #default="{ row }">
                <span class="num green">{{ fmtQuota(row.cost) }}</span>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>本月充值记录</template>
          <el-table :data="data.recharges" size="small" empty-text="本月无充值">
            <el-table-column label="时间" width="150">
              <template #default="{ row }">{{ dayjs.unix(row.created_at).format('MM-DD HH:mm') }}</template>
            </el-table-column>
            <el-table-column label="金额" width="150">
              <template #default="{ row }">{{ fmtQuota(row.amount) }}</template>
            </el-table-column>
            <el-table-column label="状态" width="80">
              <template #default="{ row }">
                <el-tag size="small" :type="row.status === 'approved' ? 'success' : row.status === 'pending' ? 'warning' : 'danger'">
                  {{ row.status === 'approved' ? '已到账' : row.status === 'pending' ? '待确认' : '已驳回' }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="voucher" label="凭证" min-width="120" show-overflow-tooltip />
          </el-table>
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>

<style scoped>
.wrap { display: flex; flex-direction: column; gap: 16px; }
.card-header { display: flex; justify-content: space-between; align-items: center; }
.green { color: var(--tg-green-ink); }
.red { color: var(--el-color-danger); }
.warn { color: var(--el-color-warning); font-weight: 600; }
.dim { color: var(--tg-muted); font-size: 12px; }
.alert-cfg { display: flex; flex-direction: column; gap: 8px; font-size: 13px; }
.monthly-line { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; font-size: 13px; margin-bottom: 12px; padding-bottom: 12px; border-bottom: 1px dashed var(--tg-line); }
.tip-inline { font-size: 12px; }
.alert-input { display: flex; align-items: center; gap: 8px; }
.tip { font-size: 12px; margin-top: 8px; }
.chain { margin-bottom: 4px; }
.sec-title { margin: 18px 0 8px; font-size: 13.5px; }
.num { font-variant-numeric: tabular-nums; }
</style>
