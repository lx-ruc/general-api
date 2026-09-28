<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { useRouter } from 'vue-router'
import { apiListOrgs, apiAddOrgQuota, apiSetOrgQuota, apiListQuotaGrants, type Org } from '../../api/platform'
import { fmtTime, fmtQuota } from '../../utils/format'

const router = useRouter()

// ---- 客户额度表（读客户列表；调整走与客户管理同一组端点）----
const list = ref<Org[]>([])
const total = ref(0)
const query = reactive({ page: 1, page_size: 20, query: '' })

async function load() {
  const resp = await apiListOrgs(query)
  list.value = resp.list
  total.value = resp.total
}
onMounted(load)

// 调整额度（按 M tokens 录入）：追加=在上限上加减（负数为冲减回收）；
// 设为=quota_limit 直接置为目标值（0=零额度），差值自动入对账流水
const M = 1_000_000
const quotaVisible = ref(false)
const quotaForm = reactive({
  org: null as Org | null, mode: 'append' as 'append' | 'set', amount: 10, target: 0, remark: '',
})
function openQuota(org: Org) {
  quotaForm.org = org
  quotaForm.mode = 'append'
  quotaForm.amount = 10
  quotaForm.target = Math.round(org.quota_limit / M)
  quotaForm.remark = ''
  quotaVisible.value = true
}
async function submitQuota() {
  if (!quotaForm.org) return
  let resp: any
  if (quotaForm.mode === 'set') {
    // input-number 清空后是 null（falsy），按 0 处理：设为 0 是合法的零额度语义
    resp = await apiSetOrgQuota(quotaForm.org.id, Math.round((quotaForm.target || 0) * M), quotaForm.remark)
  } else {
    const amount = Math.round((quotaForm.amount || 0) * M)
    if (amount === 0) {
      ElMessage.warning('追加量不能为 0（要直接改上限请切到「设为」）')
      return
    }
    resp = await apiAddOrgQuota(quotaForm.org.id, amount, quotaForm.remark)
  }
  ElMessage.success(resp?.message || '额度已调整')
  quotaVisible.value = false
  load()
  grantQuery.page = 1
  loadGrants()
}

// ---- 额度变更流水（审计：谁在什么时候给哪家客户加/减了多少）----
const grants = ref<any[]>([])
const grantTotal = ref(0)
const grantQuery = reactive({ page: 1, page_size: 10, org_id: 0 })

async function loadGrants() {
  const resp = await apiListQuotaGrants(grantQuery)
  grants.value = resp.list
  grantTotal.value = resp.total
}
onMounted(loadGrants)

function orgChanged() {
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
        <span>客户额度</span>
        <el-input v-model="query.query" placeholder="搜索客户名称" clearable style="width: 200px"
          @keyup.enter="query.page = 1; load()" />
      </div>
    </template>

    <el-table :data="list" empty-text="还没有客户。">
      <el-table-column label="客户" min-width="160">
        <template #default="{ row }">
          <el-link type="primary" @click="router.push(`/platform/orgs/${row.id}`)">{{ row.name }}</el-link>
        </template>
      </el-table-column>
      <el-table-column prop="member_count" label="成员" width="70" align="center" />
      <el-table-column label="已用 / 上限（token）" min-width="200">
        <template #default="{ row }">
          <span class="num">{{ fmtQuota(row.quota_used) }}</span>
          <span class="dim"> / {{ fmtQuota(row.quota_limit) }}</span>
        </template>
      </el-table-column>
      <el-table-column label="剩余" min-width="120">
        <template #default="{ row }">
          <span class="num" :class="row.quota_limit - row.quota_used > 0 ? 'green' : 'red'">
            {{ fmtQuota(row.quota_limit - row.quota_used) }}
          </span>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="100">
        <template #default="{ row }">
          <el-tag v-if="row.status === 1" type="success">启用</el-tag>
          <el-tag v-else-if="row.status === 2" type="danger" effect="dark">欠费停服</el-tag>
          <el-tag v-else type="danger">停用</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="120" fixed="right">
        <template #default="{ row }">
          <el-button size="small" type="primary" @click="openQuota(row)">调整额度</el-button>
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
        <span>额度变更流水</span>
        <el-select v-model="grantQuery.org_id" clearable placeholder="按客户过滤" style="width: 200px" @change="orgChanged">
          <el-option v-for="o in list" :key="o.id" :label="o.name" :value="o.id" />
        </el-select>
      </div>
    </template>

    <el-table :data="grants" empty-text="暂无流水。调整客户额度后，每一笔追加 / 冲减 / 设值差额都会记录在这里。">
      <el-table-column label="时间" width="170">
        <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
      </el-table-column>
      <el-table-column prop="org_name" label="客户" min-width="140" />
      <el-table-column label="金额（token）" min-width="140">
        <template #default="{ row }">
          <span class="num" :class="row.amount >= 0 ? 'green' : 'red'">{{ fmtAmount(row.amount) }}</span>
        </template>
      </el-table-column>
      <el-table-column prop="remark" label="事由" min-width="160" show-overflow-tooltip />
      <el-table-column label="操作人" width="110">
        <template #default="{ row }">
          <span v-if="row.operator">{{ row.operator }}</span>
          <span v-else class="dim">系统</span>
        </template>
      </el-table-column>
    </el-table>
    <el-pagination style="margin-top: 12px; justify-content: flex-end" layout="total, prev, pager, next"
      :total="grantTotal" :page-size="grantQuery.page_size" :current-page="grantQuery.page"
      @current-change="(p: number) => { grantQuery.page = p; loadGrants() }" />
  </el-card>

  <el-dialog v-model="quotaVisible" :title="`调整额度：${quotaForm.org?.name || ''}`" width="480px">
    <el-form label-width="120px">
      <el-form-item label="调整方式">
        <el-radio-group v-model="quotaForm.mode">
          <el-radio value="append">追加 / 冲减</el-radio>
          <el-radio value="set">设为</el-radio>
        </el-radio-group>
      </el-form-item>
      <el-form-item v-if="quotaForm.mode === 'append'" label="追加（M tokens）">
        <el-input-number v-model="quotaForm.amount" :step="10" />
        <span class="tip">负数为冲减回收，入对账单冲减段；= {{ fmtQuota(Math.round((quotaForm.amount || 0) * M)) }}</span>
      </el-form-item>
      <el-form-item v-else label="设为（M tokens）">
        <el-input-number v-model="quotaForm.target" :min="0" :step="10" />
        <span class="tip">当前上限 {{ fmtQuota(quotaForm.org?.quota_limit || 0) }} · 已用 {{ fmtQuota(quotaForm.org?.quota_used || 0) }}；= {{ fmtQuota(Math.round((quotaForm.target || 0) * M)) }}</span>
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
</style>
