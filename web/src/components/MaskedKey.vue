<script setup lang="ts">
// 密钥打码展示 + 复制完整密钥：列表只显示服务端打码串；
// 点「复制」调专用端点取回完整明文直接进剪贴板，界面永不回显。
import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import { copyText } from '../utils/clipboard'

const props = withDefaults(
  defineProps<{
    masked: string // 服务端打码串（前6…后4；存量仅前缀+****）
    copyable?: boolean // false=上线前存量（无加密存档），按钮置灰引导重建
    fetchFull: () => Promise<{ key: string }> // 取完整明文的 API（按属主/权限各面自备）
  }>(),
  { copyable: true },
)

const copying = ref(false)
async function copy() {
  if (!props.copyable || copying.value) return
  copying.value = true
  try {
    const { key } = await props.fetchFull()
    if (!key) {
      ElMessage.error('获取密钥失败')
      return
    }
    if (!(await copyText(key))) {
      ElMessage.error('复制失败，请检查浏览器权限')
      return
    }
    ElMessage.success('完整密钥已复制')
  } finally {
    copying.value = false
  }
}
</script>

<template>
  <span class="masked-key">
    <code class="masked-text">{{ masked || '—' }}</code>
    <el-tooltip
      v-if="!copyable"
      content="此密钥创建于功能上线前，无法复制，请删除重建"
      placement="top"
    >
      <el-button size="small" text type="info" disabled>复制</el-button>
    </el-tooltip>
    <el-button v-else size="small" text type="primary" :loading="copying" @click="copy">
      复制
    </el-button>
  </span>
</template>

<style scoped>
.masked-key {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.masked-text {
  font-family: var(--el-font-family-mono, monospace);
}
</style>
