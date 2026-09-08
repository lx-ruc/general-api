<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessageBox } from 'element-plus'
import { useAuthStore } from '../stores/auth'
import { roleNames } from '../utils/format'
import { apiChangePassword } from '../api/auth'
import { apiOrgRequests } from '../api/org'
import PlaygroundDialog from '../components/PlaygroundDialog.vue'

// 在线体验（顶栏入口）
const pgVisible = ref(false)

const auth = useAuthStore()
const router = useRouter()
const route = useRoute()

interface MenuItem {
  index: string
  title: string
  icon: string
}

const menus = computed<MenuItem[]>(() => {
  switch (auth.user?.role) {
    case 'platform_admin':
      return [
        { index: '/platform/dashboard', title: '数据看板', icon: 'Odometer' },
        { index: '/platform/orgs', title: '客户管理', icon: 'OfficeBuilding' },
        { index: '/platform/channels', title: '渠道管理', icon: 'Connection' },
        { index: '/platform/models', title: '模型定价', icon: 'PriceTag' },
        { index: '/platform/usage', title: '调用日志', icon: 'Document' },
        { index: '/platform/cost-centers', title: '成本交叉', icon: 'Coin' },
        { index: '/platform/recharges', title: '充值审批', icon: 'Wallet' },
        { index: '/platform/vendor-bills', title: '厂商对账', icon: 'Money' },
        { index: '/platform/audit', title: '审计日志', icon: 'List' },
      ]
    case 'org_admin':
      return [
        { index: '/org/dashboard', title: '客户看板', icon: 'Odometer' },
        { index: '/org/members', title: '子账号管理', icon: 'User' },
        { index: '/org/requests', title: '额度申请', icon: 'Bell' },
        { index: '/org/keys', title: '密钥一览', icon: 'Key' },
        { index: '/org/cost-centers', title: '成本中心', icon: 'Coin' },
        { index: '/org/usage', title: '调用日志', icon: 'Document' },
        { index: '/org/recharges', title: '充值', icon: 'Wallet' },
        { index: '/org/billing', title: '对账单', icon: 'Tickets' },
      ]
    default:
      return [
        { index: '/member/models', title: '可用模型', icon: 'Goods' },
        { index: '/member/keys', title: '我的密钥', icon: 'Key' },
        { index: '/member/usage', title: '我的用量', icon: 'TrendCharts' },
        { index: '/member/quota', title: '额度申请', icon: 'Bell' },
        { index: '/member/docs', title: '接入文档', icon: 'Notebook' },
      ]
  }
})

const activeMenu = computed(() => route.path)

const pageTitles: Record<string, string> = {
  '/platform/orgs/': '客户详情',
  '/docs': '文档',
}
const pageTitle = computed(() => {
  const m = menus.value.find((x) => route.path === x.index)
  if (m) return m.title
  for (const [prefix, title] of Object.entries(pageTitles)) {
    if (route.path.startsWith(prefix)) return title
  }
  return ''
})

function handleLogout() {
  auth.logout()
  router.push('/login')
}

// 待审批额度申请角标（客户管理员）
const pendingCount = ref(0)
onMounted(async () => {
  if (auth.user?.role === 'org_admin') {
    try {
      const r = await apiOrgRequests({ status: 'pending', page: 1, page_size: 1 })
      pendingCount.value = r.total || 0
    } catch { /* 拉取失败不打扰 */ }
  }
})

// 修改密码
const pwdVisible = ref(false)
const pwdForm = ref({ old_password: '', new_password: '', confirm: '' })
async function submitPassword() {
  if (!pwdForm.value.old_password || !pwdForm.value.new_password) {
    return
  }
  if (pwdForm.value.new_password !== pwdForm.value.confirm) {
    ElMessageBox.alert('两次输入的新密码不一致', '提示')
    return
  }
  await apiChangePassword(pwdForm.value.old_password, pwdForm.value.new_password)
  pwdVisible.value = false
  pwdForm.value = { old_password: '', new_password: '', confirm: '' }
  ElMessageBox.alert('密码已修改', '成功')
}
</script>

<template>
  <el-container class="layout">
    <aside class="side">
      <div class="brand">
        <span class="brand-mark" aria-hidden="true"></span>
        <div class="brand-text">
          <span class="brand-name">token 中转站</span>
          <span class="brand-sub">计量 · 转发 · 计费</span>
        </div>
      </div>

      <nav class="nav">
        <router-link v-for="m in menus" :key="m.index" :to="m.index" class="nav-item"
          :class="{ active: activeMenu === m.index }">
          <el-icon :size="16"><component :is="m.icon" /></el-icon>
          <span>{{ m.title }}</span>
          <span v-if="m.index === '/org/requests' && pendingCount > 0" class="nav-badge num">
            {{ pendingCount > 99 ? '99+' : pendingCount }}
          </span>
        </router-link>
      </nav>

      <div class="side-foot">
        <div class="meter" aria-hidden="true">
          <span class="meter-dot"></span>
          <span class="num meter-label">gateway online</span>
        </div>
      </div>
    </aside>

    <el-container class="body">
      <header class="top">
        <div class="top-left">
          <div class="topbar-brand">
            <span class="brand-dot" aria-hidden="true"></span>
            <span class="topbar-name">general api</span>
          </div>
          <span class="topbar-sep" aria-hidden="true"></span>
          <h1 class="page-title">{{ pageTitle }}</h1>
        </div>
        <div class="top-right">
          <button class="pg-btn" type="button" :class="{ on: route.path.startsWith('/docs') }" @click="router.push('/docs')">
            <el-icon :size="14"><Reading /></el-icon>
            文档
          </button>
          <button class="pg-btn" type="button" @click="pgVisible = true">
            <el-icon :size="14"><ChatDotRound /></el-icon>
            在线体验
          </button>
          <el-dropdown>
          <button class="user-chip" type="button">
            <span class="user-avatar" aria-hidden="true">{{
              (auth.user?.display_name || auth.user?.username || '?').slice(0, 1)
            }}</span>
            <span class="user-meta">
              <span class="user-name">{{ auth.user?.display_name || auth.user?.username }}</span>
              <span class="user-role">{{ auth.user?.org_name || roleNames[auth.user?.role || ''] }}</span>
            </span>
            <el-icon :size="12" color="#8a9993"><ArrowDown /></el-icon>
          </button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item @click="pwdVisible = true">修改密码</el-dropdown-item>
              <el-dropdown-item divided @click="handleLogout">退出登录</el-dropdown-item>
            </el-dropdown-menu>
          </template>
          </el-dropdown>
        </div>
      </header>

      <main class="main">
        <router-view />
      </main>
    </el-container>
  </el-container>

  <el-dialog v-model="pwdVisible" title="修改密码" width="420px">
    <el-form label-width="90px">
      <el-form-item label="原密码">
        <el-input v-model="pwdForm.old_password" type="password" show-password />
      </el-form-item>
      <el-form-item label="新密码">
        <el-input v-model="pwdForm.new_password" type="password" show-password placeholder="至少 6 位" />
      </el-form-item>
      <el-form-item label="确认新密码">
        <el-input v-model="pwdForm.confirm" type="password" show-password />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="pwdVisible = false">取消</el-button>
      <el-button type="primary" @click="submitPassword">确定</el-button>
    </template>
  </el-dialog>

  <!-- 在线体验：选模型流式试聊 -->
  <PlaygroundDialog v-model="pgVisible" />
</template>

<style scoped>
.layout { height: 100vh; }

/* ---------- 侧栏：亮色计量台 ---------- */
.side {
  width: 224px;
  background: var(--tg-sidebar);
  border-right: 1px solid var(--tg-line);
  display: flex;
  flex-direction: column;
  color: var(--tg-sidebar-ink);
}
.brand {
  display: flex; align-items: center; gap: 10px;
  padding: 20px 18px 18px;
  border-bottom: 1px solid var(--tg-line);
}
.brand-mark {
  width: 10px; height: 10px; border-radius: 50%;
  background: var(--tg-green);
  box-shadow: 0 0 0 4px rgba(18, 164, 98, 0.16);
  flex: none;
}
.brand-text { display: flex; flex-direction: column; line-height: 1.25; }
.brand-name { color: var(--tg-ink); font-size: 14.5px; font-weight: 600; letter-spacing: 0.01em; }
.brand-sub { font-size: 11px; color: var(--tg-muted); margin-top: 2px; }

.nav { flex: 1; padding: 10px 10px; display: flex; flex-direction: column; gap: 2px; overflow-y: auto; }
.nav-item {
  display: flex; align-items: center; gap: 10px;
  padding: 9px 12px; border-radius: 6px;
  color: var(--tg-sidebar-ink); font-size: 13.5px;
  text-decoration: none; position: relative;
  transition: background 0.15s, color 0.15s;
}
.nav-item:hover { background: var(--tg-green-wash); color: var(--tg-ink); }
.nav-item.active {
  background: var(--tg-green-wash-strong);
  color: #08623e; font-weight: 600;
}
.nav-item.active::before {
  content: ''; position: absolute; left: -10px; top: 8px; bottom: 8px;
  width: 3px; border-radius: 0 2px 2px 0; background: var(--tg-green);
}
.nav-item .el-icon { flex: none; }
.nav-badge {
  margin-left: auto;
  min-width: 18px; height: 18px; padding: 0 5px;
  border-radius: 9px;
  background: #c05621; color: #fff;
  font-size: 11px; line-height: 18px; text-align: center;
}

.side-foot { padding: 14px 18px; border-top: 1px solid var(--tg-line); }
.meter { display: flex; align-items: center; gap: 7px; }
.meter-dot {
  width: 6px; height: 6px; border-radius: 50%; background: var(--tg-green);
  animation: pulse 2.4s ease-in-out infinite;
}
.meter-label { font-size: 10.5px; color: var(--tg-muted); letter-spacing: 0.06em; }
@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.35; }
}
@media (prefers-reduced-motion: reduce) {
  .meter-dot { animation: none; }
}

/* ---------- 顶栏 ---------- */
.body { flex-direction: column; min-width: 0; }
.top {
  height: 60px; background: var(--tg-surface);
  border-bottom: 1px solid var(--tg-line);
  display: flex; align-items: center; justify-content: space-between;
  padding: 0 24px;
}
.top-left { display: flex; align-items: center; gap: 14px; min-width: 0; }
.top-right { display: flex; align-items: center; gap: 14px; }

.topbar-brand { display: flex; align-items: center; gap: 9px; }
.topbar-brand .brand-dot {
  width: 9px; height: 9px; border-radius: 50%;
  background: var(--tg-green);
  box-shadow: 0 0 0 3px rgba(18, 164, 98, 0.16);
}
.topbar-name {
  font-size: 15px; font-weight: 700; letter-spacing: 0.01em;
  color: var(--tg-ink);
  font-family: ui-monospace, 'SF Mono', Menlo, monospace;
}
.topbar-sep { width: 1px; height: 20px; background: var(--tg-line-strong); }

.pg-btn {
  display: inline-flex; align-items: center; gap: 6px;
  border: 1px solid var(--tg-line-strong); border-radius: 999px;
  background: var(--tg-surface); color: var(--tg-green-ink);
  font-size: 12.5px; padding: 6px 14px; cursor: pointer;
  transition: border-color 0.15s, background 0.15s;
}
.pg-btn:hover { border-color: var(--tg-green); background: var(--tg-green-wash); }
.pg-btn.on { border-color: var(--tg-green); background: var(--tg-green-wash-strong); color: #08623e; font-weight: 600; }

.page-title { font-size: 16px; font-weight: 600; margin: 0; color: var(--tg-ink); white-space: nowrap; }

.user-chip {
  display: flex; align-items: center; gap: 9px;
  background: transparent; border: 1px solid transparent; border-radius: 999px;
  padding: 4px 8px 4px 4px; cursor: pointer;
  transition: border-color 0.15s, background 0.15s;
}
.user-chip:hover { border-color: var(--tg-line); background: var(--tg-paper); }
.user-avatar {
  width: 30px; height: 30px; border-radius: 50%;
  background: var(--tg-green-wash); color: var(--tg-green-ink);
  display: flex; align-items: center; justify-content: center;
  font-size: 13px; font-weight: 600;
}
.user-meta { display: flex; flex-direction: column; align-items: flex-start; line-height: 1.2; }
.user-name { font-size: 13px; color: var(--tg-ink); }
.user-role { font-size: 11px; color: var(--tg-muted); margin-top: 1px; }

.main {
  flex: 1; overflow-y: auto; padding: 20px 24px 32px;
  background: var(--tg-paper);
}
</style>
