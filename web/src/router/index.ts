import { createRouter, createWebHashHistory, type RouteRecordRaw } from 'vue-router'
import { useAuthStore, homeOf } from '../stores/auth'

const routes: RouteRecordRaw[] = [
  { path: '/login', name: 'login', component: () => import('../views/LoginView.vue') },
  { path: '/register', name: 'register', component: () => import('../views/RegisterView.vue') },
  { path: '/', redirect: '/home' },
  {
    path: '/',
    component: () => import('../layouts/AdminLayout.vue'),
    children: [
      { path: 'home', redirect: '/platform/dashboard' },
      // 系统管理员
      { path: 'platform/dashboard', component: () => import('../views/platform/DashboardView.vue'), meta: { roles: ['platform_admin'] } },
      { path: 'platform/orgs', component: () => import('../views/platform/OrgListView.vue'), meta: { roles: ['platform_admin'] } },
      { path: 'platform/orgs/:id', component: () => import('../views/platform/OrgDetailView.vue'), meta: { roles: ['platform_admin'] } },
      { path: 'platform/channels', component: () => import('../views/platform/ChannelListView.vue'), meta: { roles: ['platform_admin'] } },
      { path: 'platform/models', component: () => import('../views/platform/ModelListView.vue'), meta: { roles: ['platform_admin'] } },
      { path: 'platform/usage', component: () => import('../views/platform/UsageView.vue'), meta: { roles: ['platform_admin'] } },
      { path: 'platform/audit', component: () => import('../views/platform/AuditView.vue'), meta: { roles: ['platform_admin'] } },
      // 客户管理员
      { path: 'org/dashboard', component: () => import('../views/org/DashboardView.vue'), meta: { roles: ['org_admin'] } },
      { path: 'org/members', component: () => import('../views/org/MemberListView.vue'), meta: { roles: ['org_admin'] } },
      { path: 'org/requests', component: () => import('../views/org/RequestListView.vue'), meta: { roles: ['org_admin'] } },
      { path: 'org/usage', component: () => import('../views/org/UsageView.vue'), meta: { roles: ['org_admin'] } },
      { path: 'org/recharges', component: () => import('../views/org/RechargeView.vue'), meta: { roles: ['org_admin'] } },
      { path: 'org/billing', component: () => import('../views/org/BillingView.vue'), meta: { roles: ['org_admin'] } },
      { path: 'org/keys', component: () => import('../views/org/KeyListView.vue'), meta: { roles: ['org_admin'] } },
      { path: 'org/cost-centers', component: () => import('../views/org/CostCenterView.vue'), meta: { roles: ['org_admin'] } },
      // 子账号
      { path: 'member/models', component: () => import('../views/member/MyModelsView.vue'), meta: { roles: ['member'] } },
      { path: 'member/keys', component: () => import('../views/member/MyKeysView.vue'), meta: { roles: ['member'] } },
      { path: 'member/usage', component: () => import('../views/member/MyUsageView.vue'), meta: { roles: ['member'] } },
      { path: 'member/quota', component: () => import('../views/member/QuotaRequestView.vue'), meta: { roles: ['member'] } },
      { path: 'member/docs', component: () => import('../views/member/DocsView.vue'), meta: { roles: ['member'] } },
      // 文档中心（所有角色可读，不设 meta.roles）
      { path: 'docs/:page?', component: () => import('../views/docs/DocsSiteView.vue') },
    ],
  },
  { path: '/:pathMatch(.*)*', redirect: '/home' },
]

const router = createRouter({
  history: createWebHashHistory(),
  routes,
})

router.beforeEach(async (to) => {
  const auth = useAuthStore()
  if (to.path === '/login' || to.path === '/register') {
    if (auth.token && to.path === '/login') return homeOf(auth.user?.role)
    return true
  }
  if (!auth.token) return '/login'
  if (!auth.user) {
    try {
      await auth.fetchMe()
    } catch {
      auth.logout()
      return '/login'
    }
  }
  const roles = to.meta.roles as string[] | undefined
  if (roles && auth.user && !roles.includes(auth.user.role)) {
    return homeOf(auth.user.role)
  }
  return true
})

export default router
