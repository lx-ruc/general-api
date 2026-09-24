import http from './http'

// ---- 客户 ----
export interface Org {
  id: number
  name: string
  remark: string
  contact_name: string
  contact_phone: string
  quota_limit: number
  quota_used: number
  monthly_quota: number   // 单月消费上限（点；0=不限）
  monthly_cost: number    // 当前账期累计（monthly_period 为当前月时有效）
  monthly_period: string
  status: number          // 1启用 0停用 2欠费停服（自动）
  created_at: number
  member_count?: number
}

export interface QuotaGrant {
  id: number
  subject_type: 'org' | 'user'
  subject_id: number
  amount: number
  remark: string
  operator_id: number | null
  created_at: number
}

export const apiListOrgs = (params?: any) => http.get<any, any>('/api/platform/orgs', { params })
export const apiCreateOrg = (data: any) => http.post<any, any>('/api/platform/orgs', data)
export const apiGetOrg = (id: number) => http.get<any, any>(`/api/platform/orgs/${id}`)
export const apiUpdateOrg = (id: number, data: any) => http.put<any, any>(`/api/platform/orgs/${id}`, data)
export const apiDeleteOrg = (id: number) => http.delete<any, any>(`/api/platform/orgs/${id}`)
export const apiAddOrgQuota = (id: number, amount: number, remark: string) =>
  http.post<any, any>(`/api/platform/orgs/${id}/quota`, { amount, remark })
// 限额设值调整：quota_limit 直接置为目标值（区别于 POST 的追加/冲减），差值自动入流水
export const apiSetOrgQuota = (id: number, limit: number, remark: string) =>
  http.put<any, any>(`/api/platform/orgs/${id}/quota`, { limit, remark })

// 客户用量统计（含每个模型的用量明细 by_model / 每个子账号 by_user）
export const apiOrgDetailStats = (id: number) => http.get<any, any>(`/api/platform/orgs/${id}/stats`)

// 重置客户管理员密码（userId 为空时取首任管理员）
export const apiResetOrgAdminPassword = (orgId: number, newPassword: string, userId?: number) =>
  http.post<any, any>(`/api/platform/orgs/${orgId}/reset-admin-password`,
    { new_password: newPassword, user_id: userId || 0 })

// ---- 渠道 ----
export interface ChannelAbility {
  channel_id?: number
  model_name: string
  upstream_model_name?: string | null
}

export interface Channel {
  id: number
  name: string
  vendor: string
  base_url: string
  path: string
  has_key?: boolean
  key_count?: number
  key_active_count?: number
  weight: number
  priority: number
  status: number
  last_test_at: number | null
  last_test_ok: number
  remark: string
  models?: ChannelAbility[]
}

export interface ChannelKeyRow {
  id: number
  key_masked: string
  weight: number
  status: number
  remark: string
  cooling: boolean
  quota_cooling: boolean
  created_at: number
  updated_at: number
}

export const apiListChannels = () => http.get<any, Channel[]>('/api/platform/channels')
// 创建返回新渠道 id：供前端创建后自动探活
export interface ChannelCreateResult { id: number; message: string; key_count: number }
export interface ChannelTestResult { ok: boolean; status: number; latency_ms: number; error?: string; key?: string; quota_exhausted?: boolean }
export const apiCreateChannel = (data: any) => http.post<any, ChannelCreateResult>('/api/platform/channels', data)
export const apiGetChannel = (id: number) => http.get<any, any>(`/api/platform/channels/${id}`)
export const apiUpdateChannel = (id: number, data: any) => http.put<any, any>(`/api/platform/channels/${id}`, data)
export const apiUpdateChannelStatus = (id: number, status: number) =>
  http.put<any, any>(`/api/platform/channels/${id}/status`, { status })
export const apiDeleteChannel = (id: number) => http.delete<any, any>(`/api/platform/channels/${id}`)
export const apiTestChannel = (id: number) => http.post<any, ChannelTestResult>(`/api/platform/channels/${id}/test`)
// 实时拉取上游模型列表（编辑渠道时供管理员挑选）
export const apiFetchUpstreamModels = (id: number) =>
  http.get<any, { models: string[]; count: number }>(`/api/platform/channels/${id}/upstream-models`)
// 表单版：建渠道前按 base_url / 路径 / 密钥直接拉（渠道尚未保存、无 id 时用）
export const apiFetchUpstreamModelsByForm = (data: { base_url: string; path: string; upstream_key: string }) =>
  http.post<any, { models: string[]; count: number }>('/api/platform/upstream-models', data)
export const apiAddChannelKeys = (id: number, keys: string[], weight: number) =>
  http.post<any, any>(`/api/platform/channels/${id}/keys`, { keys, weight })
export const apiDeleteChannelKey = (id: number, kid: number) =>
  http.delete<any, any>(`/api/platform/channels/${id}/keys/${kid}`)
export const apiListChannelKeys = (id: number) =>
  http.get<any, ChannelKeyRow[]>(`/api/platform/channels/${id}/keys`)
export const apiUpdateChannelKeyStatus = (id: number, kid: number, status: number) =>
  http.put<any, any>(`/api/platform/channels/${id}/keys/${kid}/status`, { status })
// 立即解除该 Key 的冷却（含配额标记）：厂商侧限额恢复后用，不必等指数退避自然到期
export const apiClearChannelKeyCooldown = (id: number, kid: number) =>
  http.post<any, { message: string }>(`/api/platform/channels/${id}/keys/${kid}/cooldown/clear`)

// ---- 模型 ----
export interface MModel {
  id: number
  name: string
  display_name: string
  vendor: string
  input_price: number
  output_price: number
  /** 缓存命中输入单价（点/M）；0=同 input_price */
  input_cache_hit_price: number
  cost_input_price: number
  cost_output_price: number
  cost_input_cache_hit_price: number
  status: number
  remark: string
  /** 已接通（启用且有可用密钥）的渠道数，0=仅登记未接渠道 */
  channel_count?: number
}

export const apiListModels = () => http.get<any, MModel[]>('/api/platform/models')
export const apiCreateModel = (data: any) => http.post<any, any>('/api/platform/models', data)
export const apiUpdateModel = (id: number, data: any) => http.put<any, any>(`/api/platform/models/${id}`, data)
export const apiDeleteModel = (id: number) => http.delete<any, any>(`/api/platform/models/${id}`)

// ---- 统计 / 日志 ----
export const apiStatsOverview = () => http.get<any, any>('/api/platform/stats/overview')
export const apiListUsage = (params?: any) => http.get<any, any>('/api/platform/usage', { params })

// ---- 审计 ----
export const apiListAudit = (params?: any) => http.get<any, any>('/api/platform/audit', { params })

// 额度预警：为某客户设置阈值（0 = 关闭）
export const apiUpdateOrgAlertLevels = (id: number, threshold: number) =>
  http.put<any, any>(`/api/platform/orgs/${id}/alert-levels`, { threshold })

// ---- 充值审批与收款信息 ----

export interface RechargeRow {
  id: number
  org_id: number
  org_name: string
  amount: number
  voucher: string
  status: 'pending' | 'approved' | 'rejected'
  handled_by: number | null
  handled_at: number | null
  reply: string
  created_at: number
}

export interface RechargePage { list: RechargeRow[]; total: number; page: number; page_size: number }

export const apiListRecharges = (params?: any) => http.get<any, RechargePage>('/api/platform/recharges', { params })

// 批准=额度自动到账并邮件通知客户管理员；驳回附回复说明
export const apiHandleRecharge = (id: number, action: 'approve' | 'reject', reply = '') =>
  http.put<any, { message: string }>(`/api/platform/recharges/${id}`, { action, reply })

export const apiGetBankInfo = () => http.get<any, { bank_info: string }>('/api/platform/bank-info')

export const apiUpdateBankInfo = (bankInfo: string) =>
  http.put<any, { message: string }>('/api/platform/bank-info', { bank_info: bankInfo })

// 平台视角对账单
export const apiOrgStatement = (id: number, month: string) =>
  http.get<any, any>(`/api/platform/orgs/${id}/statement`, { params: { month } })

export async function downloadOrgStatementCSVPlatform(id: number, month: string, orgName: string) {
  const resp = await http.get(`/api/platform/orgs/${id}/statement/csv`, {
    params: { month }, responseType: 'blob' as any,
  })
  const blob = resp as unknown as Blob
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = `对账单_${orgName}_${month}.csv`
  a.click()
  URL.revokeObjectURL(a.href)
}
