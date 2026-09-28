<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { apiListMembers, apiAddMemberQuota, apiUpdateMember, apiOrgStats, apiOrgQuotaGrants, type Member } from '../../api/org'
import { fmtTime, fmtQuota } from '../../utils/format'

// 客户额度池（下发的总闸：子账号消耗都在池内结算）
const org = ref<any>(null)
async function loadOrg() {
  const resp = await apiOrgStats()
  org.value = resp.org
}

// ---- 子账号额度表（读成员列表；下发走与子账号管理同一组端点）----
const list = ref<Member[]>([])
const total = ref(0)
const query = reactive({ page: 1, page_size: 20, query: '' })

async function load() {
  const resp = await apiListMembers(query)
  list.value = resp.list
  total.value = resp.total
}
onMounted(() => {
  loadOrg()
  load()
  loadGrants()
})

// 下发额度（按 M tokens 录入；负数为回收）+ 单月上限（0=不限）
const M = 1_000_000
const quotaVisible = ref(false)
const quotaForm = reactive({ member: null as Member | null, amount: 1, remark: '', monthly: 0 })
function openQuota(m: Member) {
  quotaForm.member = m
  quotaForm.amount = 1
  quotaForm.remark = ''
  quotaForm.monthly = (m.monthly_quota || 0) / M
  quotaVisible.value = true
}
async function submitQuota() {
  if (!quotaForm.member) return
  // input-number 清空后是 null（falsy）：追加 0 无意义跳过，但月限等后续步骤必须照常提交，
  // 否则「只改单月上限」点确定无任何反应
  const amount = Math.round((quotaForm.amount || 0) * M)
  if (amount !== 0) {
    await apiAddMemberQuota(quotaForm.member.id, amount, quotaForm.remark)
  }
  // 单月上限走设值更新（0=不限；与追加额度独立，总是提交保持一致）
  await apiUpdateMember(quotaForm.member.id, { monthly_quota: Math.round((quotaForm.monthly || 0) * M) || 0 })
  ElMessage.success(amount !== 0 ? '额度与月限已更新' : '单月上限已更新')
  quotaVisible.value = false
  load()
  grantQuery.page = 1
  loadGrants()
}

// ---- 下发流水（审计：谁在什么时候给哪个子账号发了多少）----
const grants = ref<any[]>([])
const grantTotal = ref(0)
const grantQuery = reactive({ page: 1, page_size: 10, user_id: 0 })

async function loadGrants() {
  const resp = await apiOrgQuotaGrants(grantQuery)
  grants.value = resp.list
  grantTotal.value = resp.total
}

function userChanged() {
  grantQuery.page = 1
  loadGrants()
}

function fmtAmount(v: number): string {
  return v > 0 ? `+${fmtQuota(v)}` : fmtQuota(v)
}
</script>

<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>客户额度池</span>
      </div>
    </template>
    <div v-if="org" class="pool">
      <div class="pool-item">
        <div class="pool-label">剩余（token）</div>
        <div class="pool-value num green">{{ fmtQuota(org.quota_limit - org.quota_used) }}</div>
      </div>
      <div class="pool-item">
        <div class="pool-label">额度上限</div>
        <div class="pool-value num">{{ fmtQuota(org.quota_limit) }}</div>
      </div>
      <div class="pool-item">
        <div class="pool-label">已消耗</div>
        <div class="pool-value num">{{ fmtQuota(org.quota_used) }}</div>
      </div>
      <div class="pool-meter">
        <div class="pool-meter-bar">
          <div class="pool-meter-fill" :style="{
            width: `${Math.max(0.8, Math.min(100, org.quota_limit ? (org.quota_used / org.quota_limit) * 100 : 0))}%`,
          }" />
        </div>
        <span class="pool-meter-label num">已用 {{ org.quota_limit ? ((org.quota_used / org.quota_limit) * 100).toFixed(2) : '0' }}%</span>
      </div>
    </div>
    <el-alert type="info" :closable="false"
      title="子账号额度是「设上限」式分配：这里下发的每一笔都入客户额度池流水，子账号实际消耗时才扣减池子；池子耗尽则全客户调用被拦截。" />
  </el-card>

  <el-card shadow="never" style="margin-top: 16px">
    <template #header>
      <div class="card-header">
        <span>子账号额度</span>
        <el-input v-model="query.query" placeholder="搜索用户名 / 姓名" clearable style="width: 200px"
          @keyup.enter="query.page = 1; load()" />
      </div>
    </template>

    <el-table :data="list" empty-text="还没有子账号。">
      <el-table-column label="子账号" min-width="150">
        <template #default="{ row }">
          {{ row.username }}
          <span v-if="row.display_name" class="dim">（{{ row.display_name }}）</span>
        </template>
      </el-table-column>
      <el-table-column label="已用 / 上限（token）" min-width="190">
        <template #default="{ row }">
          <span class="num">{{ fmtQuota(row.quota_used) }}</span>
          <span class="dim"> /
            <span v-if="row.quota_limit == null" class="unlimited">不限</span>
            <span v-else class="num">{{ fmtQuota(row.quota_limit) }}</span>
          </span>
        </template>
      </el-table-column>
      <el-table-column label="剩余" min-width="110">
        <template #default="{ row }">
          <span v-if="row.quota_limit == null" class="green">不限</span>
          <span v-else class="num" :class="row.quota_limit - row.quota_used > 0 ? 'green' : 'red'">
            {{ fmtQuota(row.quota_limit - row.quota_used) }}
          </span>
        </template>
      </el-table-column>
      <el-table-column label="单月上限" min-width="110">
        <template #default="{ row }">
          <span v-if="row.monthly_quota > 0" class="num">{{ fmtQuota(row.monthly_quota) }}</span>
          <span v-else class="dim">不限</span>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="120" fixed="right">
        <template #default="{ row }">
          <el-button size="small" type="primary" @click="openQuota(row)">下发额度</el-button>
        </template>
      </el-table-column>
    </el-table>
    <el-pagination style="margin-top: 12px; justify-content: flex-end" layout="total, prev, pager, next"
      :total="total" :page-size="query.page_size" :current-page="query.page"
      @current-change="(p: number) => { query.page = p; load() }" />
  </el-card>

  <el-card shadow="never" style="margin-top: 16px">
    <template #header>
      <div class="card-header">
        <span>下发流水</span>
        <el-select v-model="grantQuery.user_id" clearable placeholder="按子账号过滤" style="width: 200px" @change="userChanged">
          <el-option v-for="m in list" :key="m.id" :label="m.display_name ? `${m.username}（${m.display_name}）` : m.username" :value="m.id" />
        </el-select>
      </div>
    </template>

    <el-table :data="grants" empty-text="暂无流水。下发 / 回收子账号额度、审批额度申请后，每一笔都会记录在这里。">
      <el-table-column label="时间" width="170">
        <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
      </el-table-column>
      <el-table-column label="子账号" min-width="150">
        <template #default="{ row }">
          {{ row.username }}
          <span v-if="row.display_name" class="dim">（{{ row.display_name }}）</span>
        </template>
      </el-table-column>
      <el-table-column label="金额（token）" min-width="130">
        <template #default="{ row }">
          <span class="num" :class="row.amount >= 0 ? 'green' : 'red'">{{ fmtAmount(row.amount) }}</span>
        </template>
      </el-table-column>
      <el-table-column prop="remark" label="事由" min-width="160" show-overflow-tooltip />
      <el-table-column prop="operator" label="操作人" width="110" />
    </el-table>
    <el-pagination style="margin-top: 12px; justify-content: flex-end" layout="total, prev, pager, next"
      :total="grantTotal" :page-size="grantQuery.page_size" :current-page="grantQuery.page"
      @current-change="(p: number) => { grantQuery.page = p; loadGrants() }" />
  </el-card>

  <el-dialog v-model="quotaVisible" :title="`下发额度：${quotaForm.member?.username || ''}`" width="480px">
    <el-form label-width="130px">
      <el-form-item label="当前额度">
        <span v-if="quotaForm.member?.quota_limit == null" class="unlimited">不限（下面的追加在此基数上累加）</span>
        <span v-else>
          已用 {{ fmtQuota(quotaForm.member.quota_used) }} / 上限 {{ fmtQuota(quotaForm.member.quota_limit) }}
          <span v-if="quotaForm.member.quota_limit - quotaForm.member.quota_used < 0" class="red">（已超限，调用会被拦截）</span>
        </span>
      </el-form-item>
      <el-form-item label="追加（M tokens）">
        <el-input-number v-model="quotaForm.amount" :step="1" />
        <span class="tip">负数为回收；= {{ fmtQuota(Math.round((quotaForm.amount || 0) * M)) }}</span>
      </el-form-item>
      <el-form-item label="单月上限（M）">
        <el-input-number v-model="quotaForm.monthly" :min="0" :step="1" />
        <span class="tip">0 = 不限；当月消耗达到上限后拦截，次月自动恢复</span>
      </el-form-item>
      <el-form-item label="事由备注"><el-input v-model="quotaForm.remark" /></el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="quotaVisible = false">取消</el-button>
      <el-button type="primary" @click="submitQuota">确定</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.tip { margin-left: 8px; font-size: 12px; color: #909399; }
.dim { color: var(--tg-muted); font-size: 12px; }
.green { color: var(--tg-green-ink); }
.red { color: #c45656; }
.unlimited { color: var(--tg-green-ink); }

.pool {
  display: flex; align-items: center; gap: 28px; flex-wrap: wrap;
  padding: 14px 18px; margin-bottom: 14px;
  background: var(--tg-surface); border: 1px solid var(--tg-line); border-radius: 8px;
}
.pool-item { display: flex; flex-direction: column; gap: 4px; }
.pool-label { font-size: 12px; color: var(--tg-muted); }
.pool-value { font-size: 18px; font-weight: 600; color: var(--tg-ink); }
.pool-meter { display: flex; align-items: center; gap: 10px; flex: 1; min-width: 220px; }
.pool-meter-bar {
  flex: 1; height: 8px; border-radius: 4px;
  background: var(--tg-green-wash); overflow: hidden;
}
.pool-meter-fill { height: 100%; border-radius: 4px; background: var(--tg-green); }
.pool-meter-label { font-size: 12px; color: var(--tg-muted); white-space: nowrap; }
</style>
