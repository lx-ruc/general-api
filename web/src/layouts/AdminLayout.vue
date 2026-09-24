<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useAuthStore } from '../stores/auth'
import { fmtTime, roleNames } from '../utils/format'
import {
  apiChangePassword,
  apiCreateAccessToken,
  apiListAccessTokens,
  apiRevokeAccessToken,
  type AccessToken,
} from '../api/auth'
import { apiOrgRequests } from '../api/org'
import {
  apiListNotifications, apiReadNotification, apiReadAllNotifications, apiClearChannelKeyCooldown,
  type NotificationItem, type KeyQuotaPayload,
} from '../api/platform'
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
        { index: '/platform/audit', title: '审计日志', icon: 'List' },
        { index: '/platform/recharges', title: '充值审批', icon: 'Wallet' },
        { index: '/platform/demo-key', title: '在线体验', icon: 'Promotion' },
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

// 待审批额度申请角标（客户管理员）。额度申请页审批/驳回后广播
// quota-requests-changed 事件，这里监听重拉，避免角标留旧值误导
const pendingCount = ref(0)
async function refreshPending() {
  if (auth.user?.role !== 'org_admin') return
  try {
    const r = await apiOrgRequests({ status: 'pending', page: 1, page_size: 1 })
    pendingCount.value = r.total || 0
  } catch { /* 拉取失败不打扰 */ }
}
onMounted(() => {
  refreshPending()
  window.addEventListener('quota-requests-changed', refreshPending)
  if (auth.user?.role === 'platform_admin') {
    refreshNotifs()
    notifTimer = window.setInterval(refreshNotifs, 30_000)
  }
})
onUnmounted(() => {
  window.removeEventListener('quota-requests-changed', refreshPending)
  if (notifTimer) window.clearInterval(notifTimer)
})

// ---- 站内通知（仅系统管理员）：Key 配额冷却等需要处理的运营事件 ----
const notifVisible = ref(false)
const notifs = ref<NotificationItem[]>([])
const notifUnread = ref(0)
const notifClearing = ref(0)
let notifTimer: number | undefined

async function refreshNotifs() {
  if (auth.user?.role !== 'platform_admin') return
  try {
    const r = await apiListNotifications()
    notifs.value = r.list || []
    notifUnread.value = r.unread || 0
  } catch { /* 拉取失败不打扰 */ }
}

function openNotifs() {
  notifVisible.value = true
  refreshNotifs()
}

// key_quota_cooling 的 JSON 附件：定位渠道与 Key（打码），就地「清除冷却」用
function notifPayload(n: NotificationItem): KeyQuotaPayload | null {
  if (n.type !== 'key_quota_cooling') return null
  try {
    return JSON.parse(n.payload) as KeyQuotaPayload
  } catch {
    return null
  }
}

const nowSec = () => Math.floor(Date.now() / 1000)

// 点开条目即标已读（不可逆，动作就地展开在条目内）
async function readNotif(n: NotificationItem) {
  if (n.read_at) return
  try {
    await apiReadNotification(n.id)
    notifs.value = notifs.value.map((x) => (x.id === n.id ? { ...x, read_at: nowSec() } : x))
    notifUnread.value = Math.max(0, notifUnread.value - 1)
  } catch { /* 已读失败不打断浏览 */ }
}

// 厂商侧限额恢复后就地解除该 Key 的冷却，立即回轮询池
async function clearNotifCooldown(n: NotificationItem) {
  const p = notifPayload(n)
  if (!p || !p.key_id) return
  notifClearing.value = n.id
  try {
    await apiClearChannelKeyCooldown(p.channel_id, p.key_id)
    ElMessage.success(`已清除冷却：${p.channel_name} 的 Key ${p.key_masked} 已回到轮询池`)
    await readNotif(n)
  } finally {
    notifClearing.value = 0
  }
}

async function readAllNotifs() {
  await apiReadAllNotifications()
  notifs.value = notifs.value.map((n) => ({ ...n, read_at: n.read_at || nowSec() }))
  notifUnread.value = 0
}

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

// 访问令牌（仅平台/客户管理员）：程序化对接管理 API 用，权限与登录账号一致
const isAdmin = computed(() =>
  auth.user?.role === 'platform_admin' || auth.user?.role === 'org_admin')
const tokenVisible = ref(false)
const tokens = ref<AccessToken[]>([])
const tokenForm = ref({ name: '', expires_days: 0 })
const freshToken = ref('') // 新建令牌明文，仅显示一次

async function openTokens() {
  tokenVisible.value = true
  freshToken.value = ''
  await loadTokens()
}

async function loadTokens() {
  try {
    tokens.value = await apiListAccessTokens()
  } catch { /* 拉取失败提示由拦截器统一处理 */ }
}

async function createToken() {
  if (!tokenForm.value.name.trim()) return
  const r = await apiCreateAccessToken(tokenForm.value.name.trim(), tokenForm.value.expires_days)
  freshToken.value = r.token
  tokenForm.value = { name: '', expires_days: 0 }
  await loadTokens()
}

async function copyFreshToken() {
  try {
    await navigator.clipboard.writeText(freshToken.value)
    ElMessage.success('已复制')
  } catch {
    ElMessage.warning('复制失败，请手动选择复制')
  }
}

async function revokeToken(row: AccessToken) {
  try {
    await ElMessageBox.confirm(
      `确定吊销「${row.name}」？吊销后立即失效，不可恢复。`, '吊销令牌', { type: 'warning' })
  } catch { return }
  await apiRevokeAccessToken(row.id)
  ElMessage.success('已吊销')
  await loadTokens()
}
</script>

<template>
  <el-container class="layout">
    <aside class="side">
      <div class="brand">
        <span class="brand-mark" aria-hidden="true"></span>
        <div class="brand-text">
          <span class="brand-name">慧沐引擎</span>
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
          <span class="meter-label">网关在线</span>
        </div>
      </div>
    </aside>

    <el-container class="body">
      <header class="top">
        <div class="top-left">
          <div class="topbar-brand">
            <span class="brand-dot" aria-hidden="true"></span>
            <span class="topbar-name">慧沐引擎</span>
          </div>
          <span class="topbar-sep" aria-hidden="true"></span>
          <h1 class="page-title">{{ pageTitle }}</h1>
        </div>
        <div class="top-right">
          <button v-if="auth.user?.role === 'platform_admin'" class="pg-btn notif-btn" type="button"
            @click="openNotifs">
            <el-icon :size="14"><Bell /></el-icon>
            通知
            <span v-if="notifUnread > 0" class="notif-badge num">
              {{ notifUnread > 99 ? '99+' : notifUnread }}
            </span>
          </button>
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
              <el-dropdown-item v-if="isAdmin" @click="openTokens">访问令牌</el-dropdown-item>
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

  <!-- 访问令牌：程序化对接管理 API（权限 = 登录账号） -->
  <el-dialog v-model="tokenVisible" title="访问令牌" width="680px">
    <el-alert v-if="freshToken" type="warning" :closable="false" class="fresh-token">
      <p>令牌明文仅此一次显示，请立即复制保存：</p>
      <div class="fresh-row">
        <code class="fresh-code">{{ freshToken }}</code>
        <el-button size="small" type="primary" @click="copyFreshToken">复制</el-button>
      </div>
    </el-alert>

    <div class="token-create">
      <el-input v-model="tokenForm.name" placeholder="备注名（如：CI 集成）" maxlength="64" style="width: 220px" />
      <el-input-number v-model="tokenForm.expires_days" :min="0" :step="30" controls-position="right" style="width: 130px" />
      <span class="hint">天（0 = 永不过期）</span>
      <el-button type="primary" :disabled="!tokenForm.name.trim()" @click="createToken">新建令牌</el-button>
    </div>

    <el-table :data="tokens" size="small">
      <el-table-column prop="name" label="名称" min-width="120" />
      <el-table-column prop="prefix" label="前缀" width="130" />
      <el-table-column label="状态" width="80">
        <template #default="{ row }">
          <el-tag :type="row.status === 1 ? 'success' : 'info'" size="small">
            {{ row.status === 1 ? '有效' : '已吊销' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="最近使用" width="150">
        <template #default="{ row }">{{ fmtTime(row.last_used_at || null) }}</template>
      </el-table-column>
      <el-table-column label="过期时间" width="150">
        <template #default="{ row }">{{ row.expires_at ? fmtTime(row.expires_at) : '永不' }}</template>
      </el-table-column>
      <el-table-column label="操作" width="80">
        <template #default="{ row }">
          <el-button v-if="row.status === 1" link type="danger" size="small" @click="revokeToken(row)">吊销</el-button>
        </template>
      </el-table-column>
    </el-table>
  </el-dialog>

  <!-- 站内通知（系统管理员）：Key 配额冷却等事件，点开可就地清除冷却 -->
  <el-dialog v-model="notifVisible" title="站内通知" width="600px">
    <div class="notif-head">
      <span class="hint">需要处理的运营事件（Key 配额冷却每把 Key 至少间隔 30 分钟提醒一次）</span>
      <el-button v-if="notifUnread > 0" size="small" @click="readAllNotifs">全部已读</el-button>
    </div>
    <div v-if="notifs.length === 0" class="notif-empty">暂无通知</div>
    <div v-else class="notif-list">
      <div v-for="n in notifs" :key="n.id" class="notif-item" :class="{ unread: !n.read_at }" @click="readNotif(n)">
        <div class="notif-row">
          <span class="notif-dot" :class="{ on: !n.read_at }" aria-hidden="true"></span>
          <span class="notif-title">{{ n.title }}</span>
          <span class="notif-time">{{ fmtTime(n.created_at) }}</span>
        </div>
        <div class="notif-body">{{ n.body }}</div>
        <div v-if="notifPayload(n) && notifPayload(n)!.key_id" class="notif-actions">
          <el-button size="small" type="warning" plain :loading="notifClearing === n.id"
            @click.stop="clearNotifCooldown(n)">
            清除冷却（厂商侧限额已恢复时用）
          </el-button>
          <span class="hint">清除此 Key 的冷却使其立即回到轮询池；冷却到期也会由真实流量自动再探测</span>
        </div>
      </div>
    </div>
  </el-dialog>

  <!-- 在线体验：选模型流式试聊 -->
  <PlaygroundDialog v-model="pgVisible" />
</template>

<style scoped>
.layout { height: 100vh; }

/* ---------- 侧栏：亮色计量台 ---------- */
.side {
  width: 224px;
  flex: none; /* 禁止收缩：主内容超宽（长 URL/代码块撑爆 flex）时侧栏不被等比压缩 */
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

/* ---------- 访问令牌 ---------- */
.fresh-token { margin-bottom: 14px; }
.fresh-token p { margin: 0 0 8px; font-size: 12.5px; }
.fresh-row { display: flex; align-items: center; gap: 8px; }
.fresh-code {
  font-family: ui-monospace, 'SF Mono', Menlo, monospace;
  font-size: 12px; padding: 4px 8px; border-radius: 4px;
  background: var(--tg-paper); border: 1px solid var(--tg-line);
  word-break: break-all;
}
.token-create {
  display: flex; align-items: center; gap: 8px;
  margin-bottom: 14px; flex-wrap: wrap;
}
.token-create .hint { font-size: 12px; color: var(--tg-muted); }

/* ---------- 站内通知 ---------- */
.notif-btn { position: relative; }
.notif-badge {
  min-width: 16px; height: 16px; padding: 0 4px;
  border-radius: 8px;
  background: #c05621; color: #fff;
  font-size: 10.5px; line-height: 16px; text-align: center;
}
.notif-head {
  display: flex; align-items: center; justify-content: space-between;
  gap: 10px; margin-bottom: 12px;
}
.notif-head .hint { font-size: 12px; color: var(--tg-muted); }
.notif-empty { color: var(--tg-muted); font-size: 13px; padding: 24px 0; text-align: center; }
.notif-list {
  max-height: 420px; overflow-y: auto;
  display: flex; flex-direction: column; gap: 8px;
}
.notif-item {
  border: 1px solid var(--tg-line); border-radius: 8px;
  padding: 10px 12px; background: var(--tg-surface);
  cursor: pointer;
}
.notif-item.unread { border-color: rgba(192, 86, 33, 0.45); background: #fdf8f4; }
.notif-row { display: flex; align-items: center; gap: 8px; }
.notif-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--tg-line-strong); flex: none; }
.notif-dot.on { background: #c05621; box-shadow: 0 0 0 3px rgba(192, 86, 33, 0.15); }
.notif-title { font-size: 13.5px; font-weight: 600; color: var(--tg-ink); }
.notif-time { margin-left: auto; font-size: 11.5px; color: var(--tg-muted); white-space: nowrap; }
.notif-body { margin-top: 6px; font-size: 12.5px; color: var(--tg-sidebar-ink); line-height: 1.6; }
.notif-actions { margin-top: 8px; display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.notif-actions .hint { font-size: 11.5px; color: var(--tg-muted); }
</style>
