<script setup lang="ts">
import { computed, markRaw, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import './docs.css'
import IntroDoc from './IntroDoc.vue'
import QuickstartDoc from './QuickstartDoc.vue'
import FeaturesDoc from './FeaturesDoc.vue'
import ApiAuthDoc from './ApiAuthDoc.vue'
import ApiChatDoc from './ApiChatDoc.vue'
import ApiModelsDoc from './ApiModelsDoc.vue'

// 文档中心外壳：分组侧栏 + 内容区 + 上一页/下一页（导航结构对照 docs.newapi.ai）
interface DocPage {
  key: string
  title: string
  component: unknown
}

const groups: { label: string; items: DocPage[] }[] = [
  {
    label: '开始使用',
    items: [
      { key: 'intro', title: '产品介绍', component: markRaw(IntroDoc) },
      { key: 'quickstart', title: '快速开始', component: markRaw(QuickstartDoc) },
    ],
  },
  {
    label: '功能指南',
    items: [{ key: 'features', title: '核心功能', component: markRaw(FeaturesDoc) }],
  },
  {
    label: 'API 参考',
    items: [
      { key: 'api-auth', title: '鉴权与错误码', component: markRaw(ApiAuthDoc) },
      { key: 'api-chat', title: '对话补全', component: markRaw(ApiChatDoc) },
      { key: 'api-models', title: '模型列表', component: markRaw(ApiModelsDoc) },
    ],
  },
]

const flat = groups.flatMap((g) => g.items)

const route = useRoute()
const page = computed(() => (route.params.page as string) || 'intro')
const current = computed(
  () => flat.find((p) => p.key === page.value) || flat[0],
)
const comp = computed(() => current.value.component)

const idx = computed(() => flat.findIndex((p) => p.key === current.value.key))
const prev = computed(() => (idx.value > 0 ? flat[idx.value - 1] : null))
const next = computed(() => (idx.value < flat.length - 1 ? flat[idx.value + 1] : null))

// 切页回顶
const contentEl = ref<HTMLElement>()
watch(page, async () => {
  if (contentEl.value) contentEl.value.scrollTop = 0
})
</script>

<template>
  <div class="docs-shell">
    <aside class="docs-side">
      <div class="docs-side-head">
        <span class="docs-side-dot" aria-hidden="true"></span>
        <span class="docs-side-name">general api 文档</span>
      </div>
      <nav class="docs-nav">
        <div v-for="g in groups" :key="g.label" class="docs-group">
          <div class="docs-group-label">{{ g.label }}</div>
          <router-link
            v-for="p in g.items" :key="p.key" :to="`/docs/${p.key}`"
            class="docs-nav-item" :class="{ on: page === p.key }">
            {{ p.title }}
          </router-link>
        </div>
      </nav>
    </aside>

    <div ref="contentEl" class="docs-content">
      <component :is="comp" />
      <div class="doc-pager-wrap"><div class="doc-pager">
        <router-link v-if="prev" :to="`/docs/${prev.key}`">
          <span class="dir">← 上一页</span>
          <span class="name">{{ prev.title }}</span>
        </router-link>
        <span v-else aria-hidden="true"></span>
        <router-link v-if="next" :to="`/docs/${next.key}`" class="next">
          <span class="dir">下一页 →</span>
          <span class="name">{{ next.title }}</span>
        </router-link>
      </div></div>
    </div>
  </div>
</template>

<style scoped>
/* 文档中心铺满主区（抵消 AdminLayout main 的 padding）；内部双栏各自滚动 */
.docs-shell {
  margin: -20px -24px -32px;
  height: calc(100vh - 60px);
  display: flex;
  background: var(--tg-surface);
}

.docs-side {
  width: 236px; flex: none;
  border-right: 1px solid var(--tg-line);
  background: var(--tg-paper);
  display: flex; flex-direction: column;
  overflow-y: auto;
}
.docs-side-head {
  display: flex; align-items: center; gap: 9px;
  padding: 20px 20px 16px;
}
.docs-side-dot {
  width: 9px; height: 9px; border-radius: 50%;
  background: var(--tg-green);
  box-shadow: 0 0 0 3px rgba(18, 164, 98, 0.16);
}
.docs-side-name {
  font-family: ui-monospace, 'SF Mono', Menlo, monospace;
  font-size: 13.5px; font-weight: 700; color: var(--tg-ink);
}

.docs-nav { padding: 4px 12px 20px; }
.docs-group { margin-bottom: 14px; }
.docs-group-label {
  font-size: 11px; font-weight: 600; letter-spacing: 0.07em;
  color: var(--tg-muted); padding: 8px 10px 6px;
}
.docs-nav-item {
  display: block; padding: 7px 10px; border-radius: 6px;
  font-size: 13.5px; color: #3d4a44; text-decoration: none;
  transition: background 0.15s, color 0.15s;
}
.docs-nav-item:hover { background: var(--tg-green-wash); color: var(--tg-ink); }
.docs-nav-item.on {
  background: var(--tg-green-wash-strong);
  color: #08623e; font-weight: 600;
}

.docs-content { flex: 1; overflow-y: auto; min-width: 0; }

/* 窄屏：侧栏折叠为顶部横向导航 */
@media (max-width: 900px) {
  .docs-shell { flex-direction: column; height: auto; min-height: calc(100vh - 60px); }
  .docs-side {
    width: auto; flex-direction: row; align-items: center;
    overflow-x: auto; overflow-y: hidden;
    border-right: none; border-bottom: 1px solid var(--tg-line);
    padding: 10px 14px; gap: 4px;
  }
  .docs-side-head { display: none; }
  .docs-nav { display: flex; padding: 0; gap: 4px; }
  .docs-group { display: flex; align-items: center; margin: 0; }
  .docs-group-label { display: none; }
  .docs-nav-item { white-space: nowrap; padding: 6px 12px; }
}
</style>
