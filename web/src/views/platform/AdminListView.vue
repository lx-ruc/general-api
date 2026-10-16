<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  apiListAdmins, apiCreateAdmin, apiUpdateAdmin, apiResetAdminPassword,
  type AdminRow,
} from '../../api/platform'
import { fmtTime } from '../../utils/format'
import { useAuthStore } from '../../stores/auth'

// 管理员管理（超级管理员专属）：平台层账号 = 一个超级管理员 + 多个子管理员。
// 子管理员拥有平台全部业务权限；其一切写操作都会进「审计日志」。
// 自己所在行的操作由后端防自锁兜底（对自己 400），前端直接禁用按钮更直观。

const auth = useAuthStore()
const selfId = computed(() => auth.user?.id)

const list = ref<AdminRow[]>([])
const total = ref(0)
const loading = ref(false)
const query = reactive({ page: 1, page_size: 20, query: '' })

async function load() {
  loading.value = true
  try {
    const resp = await apiListAdmins(query)
    list.value = resp.list
    total.value = resp.total
  } finally {
    loading.value = false
  }
}
onMounted(load)

// 新建子管理员（建号恒为子管理员；超级位只能在列表里另行提升）
const createVisible = ref(false)
const createForm = reactive({ username: '', password: '', display_name: '', email: '' })
async function submitCreate() {
  if (createForm.username.length < 3 || createForm.password.length < 6) {
    ElMessage.warning('用户名至少 3 位，密码至少 6 位')
    return
  }
  await apiCreateAdmin({ ...createForm })
  ElMessage.success('子管理员已创建')
  createVisible.value = false
  Object.assign(createForm, { username: '', password: '', display_name: '', email: '' })
  load()
}

// 编辑（姓名/邮箱）
const editVisible = ref(false)
const editForm = reactive({ admin: null as AdminRow | null, display_name: '', email: '' })
function openEdit(a: AdminRow) {
  editForm.admin = a
  editForm.display_name = a.display_name
  editForm.email = a.email
  editVisible.value = true
}
async function submitEdit() {
  if (!editForm.admin) return
  await apiUpdateAdmin(editForm.admin.id, { display_name: editForm.display_name, email: editForm.email })
  ElMessage.success('已保存')
  editVisible.value = false
  load()
}

// 重置密码
const pwdVisible = ref(false)
const pwdForm = reactive({ admin: null as AdminRow | null, new_password: '' })
async function submitPwd() {
  if (!pwdForm.admin || pwdForm.new_password.length < 6) {
    ElMessage.warning('新密码至少 6 位')
    return
  }
  await apiResetAdminPassword(pwdForm.admin.id, pwdForm.new_password)
  ElMessage.success('密码已重置，该管理员的旧登录会话已全部失效')
  pwdVisible.value = false
  pwdForm.new_password = ''
}

function toggleStatus(a: AdminRow) {
  const act = a.status === 1 ? '停用' : '启用'
  ElMessageBox.confirm(
    `${act}子管理员「${a.display_name || a.username}」？${a.status === 1 ? '停用后立即无法登录，存量会话即时失效。' : ''}`,
    act + '账号', { type: a.status === 1 ? 'warning' : 'info' },
  )
    .then(() => apiUpdateAdmin(a.id, { status: a.status === 1 ? 0 : 1 }))
    .then(() => {
      ElMessage.success(`已${act}`)
      load()
    })
    .catch(() => {})
}

function toggleSuper(a: AdminRow) {
  const act = a.is_super === 1 ? '降为子管理员' : '提升为超级管理员'
  ElMessageBox.confirm(
    `将「${a.display_name || a.username}」${act}？`,
    '变更权限', { type: a.is_super === 1 ? 'warning' : 'info' },
  )
    .then(() => apiUpdateAdmin(a.id, { is_super: a.is_super !== 1 }))
    .then(() => {
      ElMessage.success(`已${act}`)
      load()
    })
    .catch(() => {})
}
</script>

<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>管理员管理</span>
        <div>
          <el-input v-model="query.query" placeholder="搜索用户名/姓名" clearable style="width: 180px; margin-right: 8px"
            @keyup.enter="query.page = 1; load()" @clear="query.page = 1; load()" />
          <el-button type="primary" @click="createVisible = true">新建子管理员</el-button>
        </div>
      </div>
    </template>

    <el-table :data="list" v-loading="loading"
      empty-text="平台层当前只有你一个超级管理员。可新建子管理员分担日常运营，其操作会全部记入审计日志。">
      <el-table-column prop="id" label="#" width="60" />
      <el-table-column prop="username" label="用户名" width="130" />
      <el-table-column prop="display_name" label="姓名" width="110" />
      <el-table-column label="身份" width="110">
        <template #default="{ row }">
          <el-tag :type="row.is_super === 1 ? 'danger' : 'info'" effect="plain" size="small">
            {{ row.is_super === 1 ? '超级管理员' : '子管理员' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="邮箱" min-width="170">
        <template #default="{ row }">
          <span v-if="row.email">{{ row.email }}</span>
          <span v-else class="dim">—</span>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="80">
        <template #default="{ row }">
          <el-tag :type="row.status === 1 ? 'success' : 'danger'" effect="plain" size="small">
            {{ row.status === 1 ? '启用' : '停用' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="最近登录" width="160">
        <template #default="{ row }">{{ fmtTime(row.last_login_at) }}</template>
      </el-table-column>
      <el-table-column label="创建时间" width="160">
        <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="170" fixed="right">
        <template #default="{ row }">
          <!-- 自己所在行禁用（改名走个人资料页、改密走顶栏「修改密码」）；多操作走「更多」下拉防换行（d2a752b） -->
          <div class="op-row">
            <el-button size="small" type="primary" plain :disabled="row.id === selfId" @click="openEdit(row)">编辑</el-button>
            <el-dropdown v-if="row.id !== selfId" trigger="click" @command="(cmd: string) => {
              if (cmd === 'pwd') pwdForm.admin = row, pwdVisible = true
              else if (cmd === 'toggle') toggleStatus(row)
              else if (cmd === 'super') toggleSuper(row)
            }">
              <el-button size="small" class="more-btn">
                更多<el-icon style="margin-left: 2px"><ArrowDown /></el-icon>
              </el-button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="pwd">重置密码</el-dropdown-item>
                  <el-dropdown-item command="toggle">{{ row.status === 1 ? '停用账号' : '启用账号' }}</el-dropdown-item>
                  <el-dropdown-item command="super" divided>{{ row.is_super === 1 ? '降为子管理员' : '提升为超级管理员' }}</el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </div>
        </template>
      </el-table-column>
    </el-table>
    <el-pagination style="margin-top: 12px; justify-content: flex-end" layout="total, prev, pager, next"
      :total="total" :page-size="query.page_size" :current-page="query.page"
      @current-change="(p: number) => { query.page = p; load() }" />
  </el-card>

  <el-dialog v-model="createVisible" title="新建子管理员" width="460px">
    <el-form label-width="100px">
      <el-form-item label="用户名" required><el-input v-model="createForm.username" placeholder="至少 3 位" /></el-form-item>
      <el-form-item label="初始密码" required><el-input v-model="createForm.password" show-password placeholder="至少 6 位" /></el-form-item>
      <el-form-item label="姓名"><el-input v-model="createForm.display_name" /></el-form-item>
      <el-form-item label="邮箱">
        <el-input v-model="createForm.email" type="email" placeholder="选填；绑定后可自助找回密码、接收平台告警" />
      </el-form-item>
    </el-form>
    <p class="hint">子管理员拥有平台全部业务权限（客户/渠道/定价/配额等），但不能管理管理员、也不能查看审计日志</p>
    <template #footer>
      <el-button @click="createVisible = false">取消</el-button>
      <el-button type="primary" @click="submitCreate">创建</el-button>
    </template>
  </el-dialog>

  <el-dialog v-model="editVisible" :title="`编辑：${editForm.admin?.display_name || editForm.admin?.username || ''}`" width="440px">
    <el-form label-width="90px">
      <el-form-item label="姓名"><el-input v-model="editForm.display_name" /></el-form-item>
      <el-form-item label="邮箱"><el-input v-model="editForm.email" type="email" placeholder="留空即解绑" /></el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="editVisible = false">取消</el-button>
      <el-button type="primary" @click="submitEdit">保存</el-button>
    </template>
  </el-dialog>

  <el-dialog v-model="pwdVisible" title="重置子管理员密码" width="400px">
    <el-form label-width="90px">
      <el-form-item label="子管理员">{{ pwdForm.admin?.username }}</el-form-item>
      <el-form-item label="新密码"><el-input v-model="pwdForm.new_password" show-password placeholder="至少 6 位" /></el-form-item>
    </el-form>
    <p class="hint">重置后该管理员的旧登录会话全部失效，需用新密码重新登录</p>
    <template #footer>
      <el-button @click="pwdVisible = false">取消</el-button>
      <el-button type="primary" @click="submitPwd">确定</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.dim { color: var(--tg-muted); font-size: 12px; }
.hint { margin: 0 0 8px; font-size: 12px; color: var(--tg-muted); }
.more-btn { margin-left: 8px; }
.op-row { display: flex; align-items: center; flex-wrap: nowrap; }
</style>
