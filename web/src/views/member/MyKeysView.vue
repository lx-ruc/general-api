<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { apiMyKeys, apiCreateKey, apiDeleteKey, type MyKey } from '../../api/member'
import { fmtTime } from '../../utils/format'

const list = ref<MyKey[]>([])

async function load() {
  list.value = await apiMyKeys()
}
onMounted(load)

const createVisible = ref(false)
const keyName = ref('')
const newKey = ref<string>('')
const creating = ref(false)

async function submitCreate() {
  creating.value = true
  try {
    const resp = await apiCreateKey(keyName.value || '默认密钥')
    newKey.value = resp.key
    createVisible.value = false
    keyName.value = ''
    load()
  } finally {
    creating.value = false
  }
}

async function copyKey() {
  try {
    await navigator.clipboard.writeText(newKey.value)
    ElMessage.success('已复制到剪贴板')
  } catch {
    ElMessage.warning('复制失败，请手动选择复制')
  }
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
          <el-tag :type="row.status === 1 ? 'success' : 'danger'">{{ row.status === 1 ? '启用' : '禁用' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="最近使用" width="160">
        <template #default="{ row }">{{ fmtTime(row.last_used_at) }}</template>
      </el-table-column>
      <el-table-column label="创建时间" width="160">
        <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
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
</style>
