<script setup lang="ts">
import { nextTick, onMounted, ref, watch } from 'vue'
import { apiPlaygroundModels, playgroundChat, type PGModel, type PGUsage } from '../api/playground'
import { renderMarkdown } from '../utils/markdown'

// 在线体验：顶栏入口的模型试聊对话框（SSE 流式渲染，与 /v1 数据面同链路计费）

const visible = defineModel<boolean>({ default: false })

interface ChatMsg {
  role: 'user' | 'assistant'
  content: string
}

const models = ref<PGModel[]>([])
const modelsLoading = ref(false)
const modelName = ref('')
const messages = ref<ChatMsg[]>([])
const input = ref('')
const streaming = ref(false)
const errorMsg = ref('')
const lastUsage = ref<PGUsage | null>(null)
const aborter = ref<AbortController | null>(null)
const listEl = ref<HTMLElement>()

onMounted(async () => {
  modelsLoading.value = true
  try {
    models.value = await apiPlaygroundModels()
    if (models.value.length) modelName.value = models.value[0].name
  } catch {
    errorMsg.value = '模型列表加载失败，请重试'
  } finally {
    modelsLoading.value = false
  }
})

watch(() => messages.value[messages.value.length - 1]?.content, async () => {
  await nextTick()
  if (listEl.value) listEl.value.scrollTop = listEl.value.scrollHeight
})

const currentModel = () => models.value.find((m) => m.name === modelName.value)

async function send() {
  const text = input.value.trim()
  if (!text || streaming.value) return
  if (!modelName.value) {
    errorMsg.value = '请先选择模型'
    return
  }
  errorMsg.value = ''
  lastUsage.value = null
  input.value = ''
  messages.value = [...messages.value, { role: 'user', content: text }, { role: 'assistant', content: '' }]

  streaming.value = true
  const idx = messages.value.length - 1
  const ac = new AbortController()
  aborter.value = ac
  try {
    await playgroundChat(
      { model: modelName.value, messages: messages.value.slice(0, -1) },
      {
        onDelta: (delta) => {
          messages.value[idx].content += delta
          messages.value = [...messages.value]
        },
        onUsage: (u) => { lastUsage.value = u },
        signal: ac.signal,
      },
    )
  } catch (e) {
    if ((e as Error).name !== 'AbortError') {
      errorMsg.value = (e as Error).message || '对话失败，请重试'
    }
    // 中断或失败：清掉空的助手占位，保留已生成的部分
    if (!messages.value[idx].content) {
      messages.value = messages.value.slice(0, -1)
    }
  } finally {
    streaming.value = false
    aborter.value = null
  }
}

function stop() {
  aborter.value?.abort()
}

function clearChat() {
  if (streaming.value) stop()
  messages.value = []
  errorMsg.value = ''
  lastUsage.value = null
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
    e.preventDefault()
    send()
  }
}
</script>

<template>
  <el-dialog v-model="visible" title="在线体验" width="720px" class="pg-dialog"
    :close-on-click-modal="false">
    <!-- 工具条：模型选择 + 清空 -->
    <div class="pg-bar">
      <el-select v-model="modelName" :loading="modelsLoading" size="default"
        placeholder="选择模型" style="width: 260px" :disabled="streaming">
        <el-option v-for="m in models" :key="m.id" :value="m.name" :label="m.display_name || m.name">
          <span class="pg-opt-name">{{ m.display_name || m.name }}</span>
          <span class="pg-opt-model">{{ m.name }}</span>
        </el-option>
        <template #empty>
          <div class="pg-select-empty">暂无可体验的模型：模型需已启用、渠道可用，且（子账号/客户账号）已获授权</div>
        </template>
      </el-select>
      <span v-if="currentModel()" class="pg-model-hint mono">{{ currentModel()!.name }}</span>
      <el-button size="small" :disabled="!messages.length" @click="clearChat">清空对话</el-button>
    </div>

    <!-- 消息区 -->
    <div ref="listEl" class="pg-list">
      <div v-if="!messages.length" class="pg-empty">
        <template v-if="models.length">
          选择模型后直接对话，走与 API 调用完全一致的路由与计费链路。
        </template>
        <template v-else-if="modelsLoading">正在加载模型列表…</template>
        <template v-else>
          暂无可体验的模型：需要模型已启用、渠道可用，且（子账号/客户账号）已获模型授权。
        </template>
      </div>
      <div v-for="(m, i) in messages" :key="i" class="pg-msg" :class="m.role">
        <div class="pg-bubble">
          <!-- 助手消息按 Markdown 渲染（消毒后 v-html）；用户消息保持纯文本 -->
          <template v-if="m.role === 'assistant'">
            <div class="pg-md" v-html="renderMarkdown(m.content)"></div>
            <i v-if="streaming && i === messages.length - 1" class="pg-caret pg-caret-line"></i>
          </template>
          <span v-else class="pg-text">{{ m.content }}</span>
        </div>
      </div>
    </div>

    <!-- 用量与错误 -->
    <div class="pg-status">
      <span v-if="lastUsage" class="pg-usage num">
        入 {{ lastUsage.prompt_tokens }} · 出 {{ lastUsage.completion_tokens }} tokens
      </span>
      <span v-else-if="streaming" class="dim">生成中…</span>
      <span v-if="errorMsg" class="pg-error">{{ errorMsg }}</span>
    </div>

    <!-- 输入区 -->
    <div class="pg-input">
      <textarea v-model="input" class="pg-textarea" rows="2" placeholder="输入消息，Ctrl/⌘ + Enter 发送"
        :disabled="streaming" @keydown="onKeydown"></textarea>
      <el-button v-if="!streaming" type="primary" :disabled="!input.trim() || !modelName" @click="send">
        发送
      </el-button>
      <el-button v-else type="danger" plain @click="stop">停止</el-button>
    </div>
  </el-dialog>
</template>

<style scoped>
.pg-bar { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; }
.pg-model-hint { font-size: 11.5px; color: var(--tg-muted); }
.pg-select-empty { padding: 8px 12px; font-size: 12.5px; color: var(--tg-muted); line-height: 1.6; }
.pg-opt-name { float: left; }
.pg-opt-model { float: right; font-size: 11.5px; color: var(--tg-muted); font-family: ui-monospace, 'SF Mono', Menlo, monospace; }

.pg-list {
  height: 340px; overflow-y: auto;
  border: 1px solid var(--tg-line); border-radius: 8px;
  background: var(--tg-paper);
  padding: 14px; display: flex; flex-direction: column; gap: 10px;
}
.pg-empty { margin: auto; font-size: 13px; color: var(--tg-muted); text-align: center; line-height: 1.9; padding: 0 24px; }

.pg-msg { display: flex; }
.pg-msg.user { justify-content: flex-end; }
.pg-bubble {
  max-width: 82%; border-radius: 10px; padding: 8px 12px;
  font-size: 13.5px; line-height: 1.7;
}
.pg-msg.user .pg-bubble { background: var(--tg-green-wash-strong); color: #08623e; }
.pg-msg.assistant .pg-bubble { background: var(--tg-surface); border: 1px solid var(--tg-line); color: var(--tg-ink); }
.pg-text { white-space: pre-wrap; word-break: break-word; }

/* ---------- 助手消息 Markdown 排版 ---------- */
.pg-md { font-size: 13.5px; line-height: 1.7; word-break: break-word; }
.pg-md > :last-child { margin-bottom: 0; }
.pg-md :deep(p) { margin: 0 0 0.5em; }
.pg-md :deep(h1), .pg-md :deep(h2), .pg-md :deep(h3), .pg-md :deep(h4) {
  margin: 0.9em 0 0.4em; line-height: 1.4; color: var(--tg-ink);
}
.pg-md :deep(h1) { font-size: 16.5px; }
.pg-md :deep(h2) { font-size: 15.5px; }
.pg-md :deep(h3), .pg-md :deep(h4) { font-size: 14px; }
.pg-md :deep(h1:first-child), .pg-md :deep(h2:first-child), .pg-md :deep(h3:first-child) { margin-top: 0; }
.pg-md :deep(ul), .pg-md :deep(ol) { margin: 0.3em 0 0.6em; padding-left: 1.35em; }
.pg-md :deep(li) { margin: 0.2em 0; }
.pg-md :deep(strong) { color: var(--tg-ink); }
.pg-md :deep(a) { color: var(--tg-green-ink); }
.pg-md :deep(blockquote) {
  margin: 0.5em 0; padding: 2px 0 2px 10px;
  border-left: 3px solid var(--tg-green); color: var(--tg-muted);
}
.pg-md :deep(hr) { border: none; border-top: 1px solid var(--tg-line); margin: 0.8em 0; }
.pg-md :deep(img) { max-width: 100%; border-radius: 6px; }
/* 代码统一亮色主题：无论级联如何命中，恒为深字浅底，杜绝深底浅字/浅底浅字 */
.pg-md :deep(code) {
  font-family: ui-monospace, 'SF Mono', Menlo, monospace; font-size: 12px;
  background: var(--tg-green-wash); border: 1px solid var(--tg-line);
  border-radius: 4px; padding: 1px 5px; color: #0f6b44;
}
.pg-md :deep(pre) {
  background: var(--tg-paper); border: 1px solid var(--tg-line-strong);
  border-radius: 8px; padding: 12px 14px;
  overflow-x: auto; margin: 0.5em 0;
}
.pg-md :deep(pre code), .pg-md :deep(pre) code {
  background: none; border: none; padding: 0; color: var(--tg-ink);
  font-size: 12.5px; line-height: 1.7; white-space: pre;
}
.pg-md :deep(table) { border-collapse: collapse; margin: 0.5em 0; font-size: 12.5px; max-width: 100%; display: block; overflow-x: auto; }
.pg-md :deep(th), .pg-md :deep(td) { border: 1px solid var(--tg-line); padding: 5px 10px; text-align: left; }
.pg-md :deep(th) { background: var(--tg-paper); color: var(--tg-muted); font-weight: 600; }

.pg-caret {
  display: inline-block; width: 7px; height: 14px; margin-left: 2px;
  background: var(--tg-green); vertical-align: -2px;
  animation: pg-blink 0.9s steps(1) infinite;
}
.pg-caret-line { display: block; margin: 2px 0 0; }
@keyframes pg-blink { 50% { opacity: 0; } }

.pg-status { min-height: 22px; display: flex; align-items: center; gap: 12px; margin: 8px 2px 0; }
.pg-usage { font-size: 12px; color: var(--tg-green-ink); }
.pg-error { font-size: 12.5px; color: var(--tg-red); }
.dim { font-size: 12px; color: var(--tg-muted); }

.pg-input { display: flex; gap: 10px; align-items: flex-end; margin-top: 4px; }
.pg-textarea {
  flex: 1; resize: none; box-sizing: border-box;
  border: 1px solid var(--tg-line-strong); border-radius: 8px;
  padding: 9px 12px; font-size: 13.5px; line-height: 1.6;
  color: var(--tg-ink); background: var(--tg-surface);
  font-family: inherit; transition: border-color 0.15s, box-shadow 0.15s;
}
.pg-textarea:focus {
  outline: none; border-color: var(--tg-green);
  box-shadow: 0 0 0 3px rgba(18, 164, 98, 0.14);
}
.pg-textarea:disabled { opacity: 0.6; }
</style>
