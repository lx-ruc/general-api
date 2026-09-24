<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useRouter } from 'vue-router'
import {
  apiListOrgs, apiCreateOrg, apiUpdateOrg, apiDeleteOrg, apiAddOrgQuota, apiSetOrgQuota,
  apiResetOrgAdminPassword, type Org,
} from '../../api/platform'
import { fmtTime, fmtQuota } from '../../utils/format'

const router = useRouter()
const list = ref<Org[]>([])
const total = ref(0)
const query = reactive({ page: 1, page_size: 20, query: '' })

async function load() {
  const resp = await apiListOrgs(query)
  list.value = resp.list
  total.value = resp.total
}
onMounted(load)

// 新建客户。额度统一按 M tokens 录入（1M = 1,000,000 点；1 元 = 1M 点），提交前换算成点
const M = 1_000_000
const createVisible = ref(false)
const createForm = reactive({
  name: '', remark: '', contact_name: '', contact_phone: '', quota_amount: 100,
  admin_username: '', admin_password: '', admin_display_name: '',
})
async function submitCreate() {
  if (!createForm.name || !createForm.admin_username || !createForm.admin_password) {
    ElMessage.warning('请填写客户名称、管理员账号和密码')
    return
  }
  await apiCreateOrg({ ...createForm, quota_amount: Math.round(createForm.quota_amount * M) })
  ElMessage.success('客户已创建')
  createVisible.value = false
  Object.assign(createForm, { name: '', remark: '', contact_name: '', contact_phone: '', quota_amount: 100, admin_username: '', admin_password: '', admin_display_name: '' })
  load()
}

// 调整额度（同样按 M tokens 录入）：追加=在上限上加减（负数为冲减回收）；
// 设为=quota_limit 直接置为目标值（0=零额度），差值自动入对账流水
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
}

// 重置客户管理员密码（忘记密码时的恢复路径；不指定 user_id 时取首任管理员）
const pwdVisible = ref(false)
const pwdForm = reactive({ org: null as Org | null, new_password: '' })
async function submitPwd() {
  if (!pwdForm.org) return
  if (pwdForm.new_password.length < 6) {
    ElMessage.warning('请填写至少 6 位的新密码')
    return
  }
  const resp: any = await apiResetOrgAdminPassword(pwdForm.org.id, pwdForm.new_password)
  ElMessage.success(resp?.message ? `${resp.message}（${resp.username}）` : '密码已重置')
  pwdVisible.value = false
}

async function toggleStatus(org: Org) {
  await apiUpdateOrg(org.id, { status: org.status === 1 ? 0 : 1 })
  load()
}

async function removeOrg(org: Org) {
  await ElMessageBox.confirm(
    `删除客户「${org.name}」将同时删除其全部账号与密钥（调用日志保留）。确定？`, '危险操作',
    { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
  )
  await apiDeleteOrg(org.id)
  ElMessage.success('已删除')
  load()
}
</script>

<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>客户管理</span>
        <div>
          <el-input v-model="query.query" placeholder="搜索客户名称" clearable style="width: 200px; margin-right: 8px"
            @keyup.enter="query.page = 1; load()" />
          <el-button type="primary" @click="createVisible = true">新建客户</el-button>
        </div>
      </div>
    </template>

    <el-table :data="list" empty-text="还没有客户。新建一家客户后，其管理员即可登录管理子账号与额度。">
      <el-table-column label="客户" min-width="160">
        <template #default="{ row }">
          <el-link type="primary" @click="router.push(`/platform/orgs/${row.id}`)">{{ row.name }}</el-link>
        </template>
      </el-table-column>
      <el-table-column prop="remark" label="备注" min-width="120" show-overflow-tooltip />
      <el-table-column label="联系人" min-width="110">
        <template #default="{ row }">
          <span v-if="row.contact_name || row.contact_phone">
            {{ row.contact_name }}<span v-if="row.contact_phone" class="dim"> {{ row.contact_phone }}</span>
          </span>
          <span v-else class="dim">—</span>
        </template>
      </el-table-column>
      <el-table-column prop="member_count" label="成员" width="70" align="center" />
      <el-table-column label="已用 / 上限（token）" min-width="200">
        <template #default="{ row }">
          <span class="num">{{ fmtQuota(row.quota_used) }}</span>
          <span class="dim"> / {{ fmtQuota(row.quota_limit) }}</span>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="100">
        <template #default="{ row }">
          <el-tag v-if="row.status === 1" type="success">启用</el-tag>
          <el-tag v-else-if="row.status === 2" type="danger" effect="dark">欠费停服</el-tag>
          <el-tag v-else type="danger">停用</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="创建时间" width="170">
        <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="300" fixed="right">
        <template #default="{ row }">
          <el-button size="small" @click="openQuota(row)">调整额度</el-button>
          <el-button size="small" @click="pwdForm.org = row; pwdVisible = true">重置密码</el-button>
          <el-button size="small" @click="toggleStatus(row)">{{ row.status === 1 ? '停用' : '启用' }}</el-button>
          <el-button size="small" type="danger" @click="removeOrg(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>
    <el-pagination style="margin-top: 12px; justify-content: flex-end" layout="total, prev, pager, next"
      :total="total" :page-size="query.page_size" :current-page="query.page"
      @current-change="(p: number) => { query.page = p; load() }" />
  </el-card>

  <el-dialog v-model="createVisible" title="新建客户" width="520px">
    <!-- label 列要放得下「初始额度（token）」：EP 的 label 高度固定 32px，列宽不够会折行且第二行被裁 -->
    <el-form label-width="140px">
      <el-form-item label="客户名称" required><el-input v-model="createForm.name" /></el-form-item>
      <el-form-item label="备注"><el-input v-model="createForm.remark" /></el-form-item>
      <el-form-item label="联系人"><el-input v-model="createForm.contact_name" placeholder="客户企业联系人" /></el-form-item>
      <el-form-item label="联系电话"><el-input v-model="createForm.contact_phone" /></el-form-item>
      <el-form-item label="初始额度（M tokens）">
        <el-input-number v-model="createForm.quota_amount" :min="0" :step="10" />
        <span class="tip">= {{ fmtQuota(Math.round(createForm.quota_amount * M)) }}（高价模型按单价等比多扣）</span>
      </el-form-item>
      <el-divider content-position="left">首任客户管理员</el-divider>
      <el-form-item label="管理员用户名" required><el-input v-model="createForm.admin_username" /></el-form-item>
      <el-form-item label="管理员密码" required><el-input v-model="createForm.admin_password" show-password /></el-form-item>
      <el-form-item label="管理员姓名"><el-input v-model="createForm.admin_display_name" /></el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="createVisible = false">取消</el-button>
      <el-button type="primary" @click="submitCreate">创建</el-button>
    </template>
  </el-dialog>

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

  <el-dialog v-model="pwdVisible" :title="`重置管理员密码：${pwdForm.org?.name || ''}`" width="420px">
    <el-form label-width="90px">
      <el-form-item label="客户">{{ pwdForm.org?.name }}</el-form-item>
      <el-form-item label="新密码"><el-input v-model="pwdForm.new_password" show-password placeholder="至少 6 位" /></el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="pwdVisible = false">取消</el-button>
      <el-button type="primary" @click="submitPwd">确定</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.tip { margin-left: 8px; font-size: 12px; color: #909399; }
.dim { color: var(--tg-muted); font-size: 12px; }
.green { color: var(--tg-green-ink); }
</style>
