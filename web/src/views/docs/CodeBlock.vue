<script setup lang="ts">
import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import { copyText } from '../../utils/clipboard'

// 文档代码块：语言标签 + 一键复制（样式见 docs.css 的 .doc-code 系列）
const props = defineProps<{ code: string; lang?: string }>()
const copied = ref(false)

async function copy(): Promise<void> {
  const ok = await copyText(props.code)
  if (ok) {
    copied.value = true
    setTimeout(() => (copied.value = false), 1500)
  } else {
    ElMessage.warning('复制失败，请手动选择复制')
  }
}
</script>

<template>
  <div class="doc-code">
    <div class="doc-code-bar">
      <span class="doc-lang">{{ lang || 'text' }}</span>
      <button class="doc-copy" type="button" @click="copy">{{ copied ? '已复制' : '复制' }}</button>
    </div>
    <pre><code>{{ code }}</code></pre>
  </div>
</template>
