<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { apiMyKeys, apiCreateKey, apiDeleteKey, apiMyCostCenters, apiAssignKeyCenter, type MyKey, type MyCostCenter } from '../../api/member'
import { fmtTime } from '../../utils/format'
import { copyText } from '../../utils/clipboard'

const list = ref<MyKey[]>([])
const centers = ref<MyCostCenter[]>([])

async function load() {
  list.value = await apiMyKeys()
}
onMounted(() => {
  load()
  apiMyCostCenters().then((d) => (centers.value = d || [])).catch(() => (centers.value = []))
})

const createVisible = ref(false)
const keyName = ref('')
const keyExpires = ref<string | null>(null)
const keyCenter = ref<number | null>(null)
const newKey = ref<string>('')
const creating = ref(false)
const nowSec = Math.floor(Date.now() / 1000)

// 快捷过期时刻：7/30/90 天后；留空 = 永久
const expiryShortcuts = [7, 30, 90].map((days) => ({
  text: `${days} 天后`,
  value: () => {
    const d = new Date()
    d.setDate(d.getDate() + days)
    return d
  },
}))

async function submitCreate() {
  creating.value = true
  try {
    const expiresAt = keyExpires.value ? Number(keyExpires.value) : null
    const resp = await apiCreateKey(keyName.value || '默认密钥', expiresAt, keyCenter.value)
    newKey.value = resp.key
    createVisible.value = false
    keyName.value = ''
    keyExpires.value = null
    keyCenter.value = null
    load()
  } finally {
    creating.value = false
  }
}

// 改派只影响未来消耗；历史账单按结算时快照不变
async function reassign(k: MyKey, centerID: number | null) {
  await apiAssignKeyCenter(k.id, centerID)
  ElMessage.success('已改派（历史账单不变）')
  load()
}

async function copyKey() {
  const ok = await copyText(newKey.value)
  if (ok) ElMessage.success('已复制到剪贴板')
  else ElMessage.warning('复制失败，请手动选择复制')
}

async function remove(k: MyKey) {
  await ElMessageBox.confirm(`删除密钥「${k.name || k.key_prefix}」？删除后立即失效。`, '提示', { type: 'warning' })
  await apiDeleteKey(k.id)
  ElMessage.success('已删除')
  load()
}
</script>

<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>我的 API 密钥</span>
        <el-button type="primary" @click="createVisible = true">新建密钥</el-button>
      </div>
    </template>

    <el-table :data="list">
      <el-table-column prop="id" label="#" width="60" />
      <el-table-column prop="name" label="名称" min-width="120" />
      <el-table-column label="密钥" width="160">
        <template #default="{ row }"><code>{{ row.key_prefix }}…</code></template>
      </el-table-column>
      <el-table-column label="状态" width="80">
        <template #default="{ row }">
          <el-tag v-if="row.expired_at && row.expired_at <= nowSec" type="danger">已过期</el-tag>
          <el-tag v-else :type="row.status === 1 ? 'success' : 'danger'">{{ row.status === 1 ? '启用' : '禁用' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="有效期" width="170">
        <template #default="{ row }">
          <span v-if="!row.expired_at">永久</span>
          <el-tag v-else-if="row.expired_at <= nowSec" type="danger" size="small">{{ fmtTime(row.expired_at) }}</el-tag>
          <el-tag v-else-if="row.expired_at - nowSec <= 7 * 86400" type="warning" size="small">
            {{ fmtTime(row.expired_at) }}
          </el-tag>
          <span v-else>{{ fmtTime(row.expired_at) }}</span>
        </template>
      </el-table-column>
      <el-table-column label="最近使用" width="160">
        <template #default="{ row }">{{ fmtTime(row.last_used_at) }}</template>
      </el-table-column>
      <el-table-column label="创建时间" width="160">
        <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
      </el-table-column>
      <el-table-column label="归集中心" width="150">
        <template #default="{ row }">
          <el-select v-if="centers.length" :model-value="row.cost_center_id" size="small" style="width: 120px"
            placeholder="未归集" clearable @change="(v: any) => reassign(row, v ?? null)">
            <el-option v-for="cc in centers" :key="cc.id" :label="cc.name" :value="cc.id" />
          </el-select>
          <span v-else class="dim">-</span>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="90" fixed="right">
        <template #default="{ row }">
          <el-button size="small" type="danger" @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>
  </el-card>

  <el-dialog v-model="createVisible" title="新建 API 密钥" width="440px">
    <el-form label-width="70px">
      <el-form-item label="名称"><el-input v-model="keyName" placeholder="如：我的测试脚本" /></el-form-item>
      <el-form-item label="有效期">
        <el-date-picker v-model="keyExpires" type="datetime" placeholder="永久有效" value-format="X"
          :shortcuts="expiryShortcuts" style="width: 100%" />
        <div class="form-tip">留空 = 永久有效；到期的密钥调用将被拒绝</div>
      </el-form-item>
      <el-form-item v-if="centers.length" label="归集">
        <el-select v-model="keyCenter" placeholder="未归集" clearable style="width: 100%">
          <el-option v-for="cc in centers" :key="cc.id" :label="cc.name" :value="cc.id" />
        </el-select>
        <div class="form-tip">成本中心用于客户按项目核算；改派不影响历史账单</div>
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="createVisible = false">取消</el-button>
      <el-button type="primary" :loading="creating" @click="submitCreate">创建</el-button>
    </template>
  </el-dialog>

  <!-- 明文密钥仅显示一次 -->
  <el-dialog :model-value="!!newKey" title="密钥已创建" width="560px" :close-on-click-modal="false"
    @close="newKey = ''">
    <el-alert type="warning" :closable="false" show-icon
      title="请立即复制保存，关闭后将无法再次查看完整密钥" style="margin-bottom: 12px" />
    <el-input :model-value="newKey" readonly>
      <template #append>
        <el-button @click="copyKey">复制</el-button>
      </template>
    </el-input>
    <template #footer>
      <el-button type="primary" @click="newKey = ''">我已保存</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.form-tip { font-size: 12px; color: var(--el-text-color-secondary); line-height: 1.4; margin-top: 4px; }
.dim { color: var(--el-text-color-secondary); }
</style>
