<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import dayjs from 'dayjs'
import {
  apiListMembers, apiCreateMember, apiUpdateMember, apiDeleteMember,
  apiResetMemberPassword, apiAddMemberQuota, apiGetMemberModels, apiSetMemberModels,
  type Member,
} from '../../api/org'
import { fmtTime, fmtQuota } from '../../utils/format'

// 月累计仅在存储账期为当前月时有效（跨月惰性清零的读侧）
function currentMonthUsed(m: Member): number {
  return m.monthly_period === dayjs().format('YYYY-MM') ? m.monthly_cost : 0
}

const list = ref<Member[]>([])
const total = ref(0)
const loading = ref(false)
const query = reactive({ page: 1, page_size: 20, query: '' })

async function load() {
  loading.value = true
  try {
    const resp = await apiListMembers(query)
    list.value = resp.list
    total.value = resp.total
  } finally {
    loading.value = false
  }
}
onMounted(load)

// 新建子账号
const createVisible = ref(false)
const createForm = reactive({ username: '', password: '', display_name: '', quota_amount: 0 })
async function submitCreate() {
  if (!createForm.username || !createForm.password) {
    ElMessage.warning('请填写用户名和密码')
    return
  }
  await apiCreateMember(createForm)
  ElMessage.success('子账号已创建')
  createVisible.value = false
  Object.assign(createForm, { username: '', password: '', display_name: '', quota_amount: 0 })
  load()
}

// 额度 / 月限
const quotaVisible = ref(false)
const quotaForm = reactive({ member: null as Member | null, amount: 1000000, remark: '', monthly: 0 })
function openQuota(m: Member) {
  quotaForm.member = m
  quotaForm.amount = 1000000
  quotaForm.remark = ''
  quotaForm.monthly = m.monthly_quota || 0
  quotaVisible.value = true
}
async function submitQuota() {
  if (!quotaForm.member || !quotaForm.amount) return
  await apiAddMemberQuota(quotaForm.member.id, quotaForm.amount, quotaForm.remark)
  // 单月上限走设值更新（0=不限；与追加额度独立，总是提交保持一致）
  await apiUpdateMember(quotaForm.member.id, { monthly_quota: quotaForm.monthly || 0 })
  ElMessage.success('额度与月限已更新')
  quotaVisible.value = false
  load()
}

// 重置密码
const pwdVisible = ref(false)
const pwdForm = reactive({ member: null as Member | null, new_password: '' })
async function submitPwd() {
  if (!pwdForm.member || !pwdForm.new_password) return
  await apiResetMemberPassword(pwdForm.member.id, pwdForm.new_password)
  ElMessage.success('密码已重置')
  pwdVisible.value = false
}

// 模型授权
const grantVisible = ref(false)
const grantMember = ref<Member | null>(null)
const grantedModels = ref<string[]>([])
const availableModels = ref<any[]>([])
async function openGrant(m: Member) {
  grantMember.value = m
  const resp = await apiGetMemberModels(m.id)
  grantedModels.value = resp.granted
  availableModels.value = resp.available
  grantVisible.value = true
}
async function submitGrant() {
  if (!grantMember.value) return
  await apiSetMemberModels(grantMember.value.id, grantedModels.value)
  ElMessage.success('授权已更新')
  grantVisible.value = false
  load()
}

function toggleStatus(m: Member) {
  apiUpdateMember(m.id, { status: m.status === 1 ? 0 : 1 }).then(load)
}
function setUnlimited(m: Member, unlimited: boolean) {
  apiUpdateMember(m.id, { quota_unlimited: unlimited }).then(() => {
    ElMessage.success(unlimited ? '已设为不限额' : '已转为限额')
    load()
  })
}
function remove(m: Member) {
  ElMessageBox.confirm(`删除子账号「${m.display_name || m.username}」及其全部密钥？`, '危险操作', { type: 'warning' })
    .then(() => apiDeleteMember(m.id))
    .then(() => {
      ElMessage.success('已删除')
      load()
    })
    .catch(() => {})
}
</script>

<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>子账号管理</span>
        <div>
          <el-input v-model="query.query" placeholder="搜索用户名/姓名" clearable style="width: 180px; margin-right: 8px"
            @keyup.enter="query.page = 1; load()" @clear="query.page = 1; load()" />
          <el-button type="primary" @click="createVisible = true">新建子账号</el-button>
        </div>
      </div>
    </template>

    <el-table :data="list" v-loading="loading"
      empty-text="还没有子账号。新建子账号并授权模型后，子账号即可创建密钥调用 API。">
      <el-table-column prop="username" label="用户名" width="120" />
      <el-table-column prop="display_name" label="姓名" width="100" />
      <el-table-column label="已用 / 上限（token）" min-width="180">
        <template #default="{ row }">
          <span class="num">{{ fmtQuota(row.quota_used) }}</span>
          <span class="dim"> / </span>
          <span v-if="row.quota_limit == null" class="unlimited">不限</span>
          <span v-else class="num">{{ fmtQuota(row.quota_limit) }}</span>
        </template>
      </el-table-column>
      <el-table-column label="单月上限" width="130">
        <template #default="{ row }">
          <span v-if="row.monthly_quota > 0">
            <span class="num">{{ fmtQuota(row.monthly_quota) }}</span>
            <span class="dim"> · 已用 {{ fmtQuota(currentMonthUsed(row)) }}</span>
          </span>
          <span v-else class="dim">—</span>
        </template>
      </el-table-column>
      <el-table-column label="剩余" width="110" align="right">
        <template #default="{ row }">
          <span v-if="row.quota_limit == null" class="green">不限</span>
          <span v-else class="num" :class="row.quota_limit - row.quota_used > 0 ? 'green' : 'red'">
            {{ fmtQuota(row.quota_limit - row.quota_used) }}
          </span>
        </template>
      </el-table-column>
      <el-table-column prop="grant_count" label="授权模型" width="90" align="center" />
      <el-table-column prop="key_count" label="密钥数" width="80" align="center" />
      <el-table-column label="状态" width="80">
        <template #default="{ row }">
          <el-tag :type="row.status === 1 ? 'success' : 'danger'" effect="plain" size="small">
            {{ row.status === 1 ? '启用' : '停用' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="创建时间" width="160">
        <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="210" fixed="right">
        <template #default="{ row }">
          <el-button size="small" type="primary" plain @click="openGrant(row)">模型授权</el-button>
          <el-button size="small" @click="openQuota(row)">额度</el-button>
          <el-dropdown trigger="click" @command="(cmd: string) => {
            if (cmd === 'pwd') pwdForm.member = row, pwdVisible = true
            else if (cmd === 'toggle') toggleStatus(row)
            else if (cmd === 'unlimited') setUnlimited(row, true)
            else if (cmd === 'limit') setUnlimited(row, false)
            else if (cmd === 'delete') remove(row)
          }">
            <el-button size="small" class="more-btn">
              更多<el-icon style="margin-left: 2px"><ArrowDown /></el-icon>
            </el-button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="pwd">重置密码</el-dropdown-item>
                <el-dropdown-item command="toggle">{{ row.status === 1 ? '停用账号' : '启用账号' }}</el-dropdown-item>
                <el-dropdown-item v-if="row.quota_limit != null" command="unlimited" divided>设为不限额</el-dropdown-item>
                <el-dropdown-item v-else command="limit">设为限额</el-dropdown-item>
                <el-dropdown-item command="delete" class="danger-item">删除子账号</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </template>
      </el-table-column>
    </el-table>
    <el-pagination style="margin-top: 12px; justify-content: flex-end" layout="total, prev, pager, next"
      :total="total" :page-size="query.page_size" :current-page="query.page"
      @current-change="(p: number) => { query.page = p; load() }" />
  </el-card>

  <el-dialog v-model="createVisible" title="新建子账号" width="460px">
    <el-form label-width="100px">
      <el-form-item label="用户名" required><el-input v-model="createForm.username" /></el-form-item>
      <el-form-item label="初始密码" required><el-input v-model="createForm.password" show-password placeholder="至少 6 位" /></el-form-item>
      <el-form-item label="姓名"><el-input v-model="createForm.display_name" /></el-form-item>
      <el-form-item label="初始额度（token）">
        <el-input-number v-model="createForm.quota_amount" :min="0" :step="1000000" />
        <span class="tip">0 = 不限额</span>
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="createVisible = false">取消</el-button>
      <el-button type="primary" @click="submitCreate">创建</el-button>
    </template>
  </el-dialog>

  <el-dialog v-model="quotaVisible" :title="`额度 / 月限：${quotaForm.member?.display_name || quotaForm.member?.username || ''}`" width="440px">
    <el-form label-width="100px">
      <el-form-item label="追加token 数">
        <el-input-number v-model="quotaForm.amount" :step="1000000" />
        <span class="tip">负数为回收</span>
      </el-form-item>
      <el-form-item label="单月上限（token）">
        <el-input-number v-model="quotaForm.monthly" :min="0" :step="1000000" />
        <span class="tip">0 = 不限；当月达限停用，次月自动清零</span>
      </el-form-item>
      <el-form-item label="备注"><el-input v-model="quotaForm.remark" /></el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="quotaVisible = false">取消</el-button>
      <el-button type="primary" @click="submitQuota">确定</el-button>
    </template>
  </el-dialog>

  <el-dialog v-model="pwdVisible" title="重置子账号密码" width="400px">
    <el-form label-width="90px">
      <el-form-item label="子账号">{{ pwdForm.member?.username }}</el-form-item>
      <el-form-item label="新密码"><el-input v-model="pwdForm.new_password" show-password placeholder="至少 6 位" /></el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="pwdVisible = false">取消</el-button>
      <el-button type="primary" @click="submitPwd">确定</el-button>
    </template>
  </el-dialog>

  <el-dialog v-model="grantVisible" :title="`模型授权：${grantMember?.display_name || grantMember?.username || ''}`" width="560px">
    <p class="grant-hint">
      勾选该子账号可用 API key 调用的模型（决定其 <code>/v1/models</code> 列表与转发白名单）
    </p>
    <el-checkbox-group v-model="grantedModels">
      <el-checkbox v-for="m in availableModels" :key="m.name" :value="m.name" class="grant-item">
        <code>{{ m.name }}</code>
        <span v-if="m.display_name" class="grant-price">{{ m.display_name }}</span>
      </el-checkbox>
    </el-checkbox-group>
    <template #footer>
      <el-button @click="grantVisible = false">取消</el-button>
      <el-button type="primary" @click="submitGrant">保存授权</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.tip { margin-left: 8px; font-size: 12px; color: #909399; }
.dim { color: var(--tg-muted); font-size: 12px; }
.green { color: var(--tg-green-ink); }
.red { color: var(--tg-red); }
.unlimited { color: var(--tg-muted); font-size: 12px; }
.more-btn { margin-left: 8px; }
:deep(.danger-item) { color: var(--tg-red); }
.grant-hint { margin: 0 0 14px; font-size: 13px; color: var(--tg-graphite); }
.grant-item { width: 100%; margin-bottom: 6px; height: auto; }
.grant-price { color: var(--tg-muted); font-size: 12px; margin-left: 6px; }
</style>
