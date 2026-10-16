// 平台层管理操作 → 中文动作名（操作审计页「动作」列用）。
// 模板段以 : 开头匹配任意单段（:id/:kid/:name/:orgId 统一处理）；
// 未命中回退 "METHOD path" 原样展示（org/member 层与 /me 层写操作走回退）。
interface ActionTemplate {
  method: string
  segments: string[]
  label: string
}

const T = (method: string, path: string, label: string): ActionTemplate => ({
  method,
  segments: path.split('/').filter(Boolean),
  label,
})

// 覆盖 /api/platform 全部写操作（与 internal/api/router.go 装配表同步维护）
const TEMPLATES: ActionTemplate[] = [
  // 客户
  T('POST', '/orgs', '新建客户'),
  T('PUT', '/orgs/:id', '修改客户'),
  T('DELETE', '/orgs/:id', '删除客户'),
  T('POST', '/orgs/:id/quota', '追加客户额度'),
  T('PUT', '/orgs/:id/quota', '设置客户额度'),
  T('PUT', '/orgs/:id/alert-levels', '修改客户预警阈值'),
  T('POST', '/orgs/:id/reset-admin-password', '重置客户管理员密码'),
  T('PUT', '/orgs/:id/models', '设置客户模型开通'),
  // 账单
  T('POST', '/billing/snapshots', '手动生成余额快照'),
  T('PUT', '/vendor-bills', '保存厂商账单'),
  T('DELETE', '/vendor-bills/:id', '删除厂商账单'),
  // 渠道
  T('POST', '/channels', '新建渠道'),
  T('PUT', '/channels/:id', '修改渠道'),
  T('PUT', '/channels/:id/status', '启停渠道'),
  T('DELETE', '/channels/:id', '删除渠道'),
  T('POST', '/channels/:id/test', '测试渠道'),
  T('POST', '/upstream-models', '拉取上游模型'),
  T('POST', '/channels/:id/keys', '添加渠道 Key'),
  T('DELETE', '/channels/:id/keys/:kid', '删除渠道 Key'),
  T('PUT', '/channels/:id/keys/:kid/status', '启停渠道 Key'),
  T('POST', '/channels/:id/keys/:kid/cooldown/clear', '清除渠道 Key 冷却'),
  // 模型与定价
  T('POST', '/models', '新建模型'),
  T('PUT', '/models/:id', '修改模型定价'),
  T('DELETE', '/models/:id', '删除模型'),
  T('PUT', '/model-prices/:name', '设置客户级定价'),
  T('DELETE', '/model-prices/:name/:orgId', '删除客户级定价'),
  T('PUT', '/model-requests/:id', '审批模型申请'),
  // 充值与配置
  T('PUT', '/recharges/:id', '审批充值'),
  T('PUT', '/bank-info', '修改收款信息'),
  T('PUT', '/demo-key', '配置在线体验'),
  T('POST', '/demo-key/rotate', '轮换体验密钥'),
  T('POST', '/demo-key/copy', '复制体验密钥'),
  // 子管理员管理（超管专属）
  T('POST', '/admins', '新建子管理员'),
  T('PUT', '/admins/:id', '管理子管理员'),
  T('POST', '/admins/:id/reset-password', '重置子管理员密码'),
]

function segmentsOf(path: string): string[] {
  return path.split('?')[0].split('/').filter((s) => s !== '' && s !== 'api' && s !== 'platform')
}

function match(t: ActionTemplate, method: string, segs: string[]): boolean {
  if (t.method !== method || t.segments.length !== segs.length) return false
  for (let i = 0; i < segs.length; i++) {
    const ts = t.segments[i]
    if (ts.startsWith(':')) continue // 占位段匹配任意单段
    if (ts !== segs[i]) return false
  }
  return true
}

export function actionName(method: string, path: string): string {
  const segs = segmentsOf(path)
  for (const t of TEMPLATES) {
    if (match(t, method, segs)) return t.label
  }
  return `${method} ${path}`
}
