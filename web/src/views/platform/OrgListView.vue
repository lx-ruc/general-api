<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useRouter } from 'vue-router'
import {
  apiListOrgs, apiCreateOrg, apiUpdateOrg, apiDeleteOrg, apiAddOrgQuota, type Org,
} from '../../api/platform'
import { fmtTime, fmtQuota, pointsToYuan } from '../../utils/format'

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

// 新建客户
const createVisible = ref(false)
const createForm = reactive({
  name: '', remark: '', contact_name: '', contact_phone: '', quota_amount: 100000000,
  admin_username: '', admin_password: '', admin_display_name: '',
})
async function submitCreate() {
  if (!createForm.name || !createForm.admin_username || !createForm.admin_password) {
    ElMessage.warning('请填写客户名称、管理员账号和密码')
    return
  }
  await apiCreateOrg(createForm)
  ElMessage.success('客户已创建')
  createVisible.value = false
  Object.assign(createForm, { name: '', remark: '', contact_name: '', contact_phone: '', quota_amount: 100000000, admin_username: '', admin_password: '', admin_display_name: '' })
  load()
}

// 追加额度
const quotaVisible = ref(false)
const quotaForm = reactive({ org: null as Org | null, amount: 10000000, remark: '' })
function openQuota(org: Org) {
  quotaForm.org = org
  quotaForm.amount = 10000000
  quotaForm.remark = ''
  quotaVisible.value = true
}
async function submitQuota() {
  if (!quotaForm.org || !quotaForm.amount) return
  await apiAddOrgQuota(quotaForm.org.id, quotaForm.amount, quotaForm.remark)
  ElMessage.success('额度已追加')
  quotaVisible.value = false
  load()
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
          <span class="green num">　¥{{ pointsToYuan(row.quota_limit - row.quota_used) }} 可用</span>
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
      <el-table-column label="操作" width="230" fixed="right">
        <template #default="{ row }">
          <el-button size="small" @click="openQuota(row)">追加额度</el-button>
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
      <el-form-item label="初始额度（token）">
        <el-input-number v-model="createForm.quota_amount" :min="0" :step="10000000" />
        <span class="tip">= ¥{{ pointsToYuan(createForm.quota_amount) }}</span>
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

  <el-dialog v-model="quotaVisible" :title="`追加 / 冲减额度：${quotaForm.org?.name || ''}`" width="440px">
    <el-form label-width="100px">
      <el-form-item label="token 数">
        <el-input-number v-model="quotaForm.amount" :step="10000000" />
        <span class="tip">= ¥{{ pointsToYuan(quotaForm.amount) }}（负数为冲减回收，入对账单冲减段）</span>
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
</style>
