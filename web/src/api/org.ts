import http from './http'

export interface Member {
  id: number
  org_id: number
  username: string
  display_name: string
  email?: string // 绑定邮箱（空/未定义=未绑定）
  role: string
  quota_limit: number | null
  quota_used: number
  monthly_quota: number   // 单月消费上限（点；0=不限）
  monthly_cost: number    // 当前账期累计（monthly_period 为当前月时有效）
  monthly_period: string
  status: number
  grant_count?: number
  key_count?: number
  created_at: number
}

export interface OrgKey {
  id: number
  name: string
  key_prefix: string
  key_masked?: string // 打码展示（复制走 apiCopyOrgKey）
  key_copyable?: boolean // false=上线前存量，复制置灰
  status: number
  user_id: number
  username?: string
  cost_center_id: number | null
  cost_center_name?: string
  last_used_at: number | null
  created_at: number
}

export interface CostCenter {
  id: number
  org_id: number
  name: string
  status: number
  month_cost?: number
  key_count?: number
  created_at: number
}

export interface CostReportRow {
  cost_center_id: number | null
  center_name: string
  center_status: number
  model_name: string
  day: string
  requests: number
  cache_hits: number
  prompt_tokens: number
  completion_tokens: number
  cost: number
}

// 申请单双形态：kind=quota 额度申请 / kind=model 模型授权申请（model_names 逗号分隔）
export interface QuotaRequestRow {
  id: number
  org_id: number
  user_id: number
  username?: string
  kind: 'quota' | 'model' | ''
  amount: number
  model_names: string
  reason: string
  status: 'pending' | 'approved' | 'rejected'
  reply: string
  created_at: number
  handled_at: number | null
}

export const apiListMembers = (params?: any) => http.get<any, any>('/api/org/members', { params })
export const apiCreateMember = (data: any) => http.post<any, any>('/api/org/members', data)
export const apiUpdateMember = (id: number, data: any) => http.put<any, any>(`/api/org/members/${id}`, data)
export const apiDeleteMember = (id: number) => http.delete<any, any>(`/api/org/members/${id}`)
export const apiResetMemberPassword = (id: number, new_password: string) =>
  http.post<any, any>(`/api/org/members/${id}/reset-password`, { new_password })
export const apiAddMemberQuota = (id: number, amount: number, remark: string) =>
  http.post<any, any>(`/api/org/members/${id}/quota`, { amount, remark })
export const apiGetMemberModels = (id: number) => http.get<any, any>(`/api/org/members/${id}/models`)
export const apiSetMemberModels = (id: number, models: string[]) =>
  http.put<any, any>(`/api/org/members/${id}/models`, { models })

export const apiOrgStats = () => http.get<any, any>('/api/org/stats/overview')
export const apiOrgUsage = (params?: any) => http.get<any, any>('/api/org/usage', { params })
export const apiOrgKeys = (params?: any) => http.get<any, any>('/api/org/keys', { params })
export const apiUpdateOrgKeyStatus = (id: number, status: number) =>
  http.put<any, any>(`/api/org/keys/${id}/status`, { status })
// 复制完整密钥（本客户成员的 key，org 隔离）：明文只进剪贴板不回显界面
export const apiCopyOrgKey = (id: number): Promise<{ key: string }> =>
  http.post<any, { key: string }>(`/api/org/keys/${id}/copy`)

// ---- 成本中心 ----
export const apiOrgCostCenters = () => http.get<any, { list: CostCenter[] }>('/api/org/cost-centers')
export const apiCreateCostCenter = (name: string) => http.post<any, any>('/api/org/cost-centers', { name })
export const apiUpdateCostCenter = (id: number, data: { name?: string; status?: number }) =>
  http.put<any, any>(`/api/org/cost-centers/${id}`, data)
export const apiReassignKeyCenter = (id: number, cost_center_id: number | null) =>
  http.put<any, any>(`/api/org/keys/${id}/cost-center`, { cost_center_id })
export const apiCostCenterReport = (params?: any) =>
  http.get<any, {
    list: CostReportRow[]
    total_cost: number
    unallocated_cost: number
    unallocated_pct: number
  }>('/api/org/reports/cost-centers', { params })

export const apiOrgRequests = (params?: any) => http.get<any, any>('/api/org/requests', { params })
export const apiHandleRequest = (id: number, action: 'approve' | 'reject', reply: string) =>
  http.put<any, any>(`/api/org/requests/${id}`, { action, reply })

// ---- 模型开通申请（向平台） ----
export interface ModelRequestCandidate {
  name: string
  display_name: string
  vendor: string
  granted: boolean
  pending: boolean
  /** 平台侧当前是否有可用上游密钥；false = 已接渠道但密钥失效/未配置 */
  routable?: boolean
}

export interface OrgModelRequestRow {
  id: number
  org_id: number
  model_names: string
  reason: string
  status: 'pending' | 'approved' | 'rejected'
  reply: string
  created_at: number
  handled_at: number | null
}

export const apiModelRequestCandidates = () =>
  http.get<any, ModelRequestCandidate[]>('/api/org/model-requests/candidates')
export const apiOrgModelRequests = () =>
  http.get<any, OrgModelRequestRow[]>('/api/org/model-requests')
export const apiCreateModelRequest = (models: string[], reason: string) =>
  http.post<any, any>('/api/org/model-requests', { models, reason })

// ---- 充值 / 账单 ----
export const apiOrgRecharges = () => http.get<any, any>('/api/org/recharges')
export const apiCreateRecharge = (amount: number, voucher: string) =>
  http.post<any, any>('/api/org/recharges', { amount, voucher })
export const apiOrgBankInfo = () => http.get<any, any>('/api/org/bank-info')
export const apiOrgBilling = (month: string) => http.get<any, any>('/api/org/billing', { params: { month } })

// 额度预警：读取本客户双阈值与状态（threshold 百分比 / alert_balance 剩余额度金额，点）
export const apiGetAlertLevels = () => http.get<any, any>('/api/org/alert-levels')
// 额度预警：设置双阈值（均 0 = 关闭，两项独立开关；alert_balance 传点数，前端按元录入后换算）
export const apiUpdateAlertLevels = (threshold: number, alertBalancePoints?: number) =>
  http.put<any, any>('/api/org/alert-levels',
    alertBalancePoints === undefined ? { threshold } : { threshold, alert_balance: alertBalancePoints })

// 子账号个人预警：读取/设置双阈值（org 隔离，仅本客户成员）
export const apiGetMemberAlertLevels = (id: number) =>
  http.get<any, any>(`/api/org/members/${id}/alert-levels`)
export const apiUpdateMemberAlertLevels = (id: number, threshold: number, alertBalancePoints?: number) =>
  http.put<any, any>(`/api/org/members/${id}/alert-levels`,
    alertBalancePoints === undefined ? { threshold } : { threshold, alert_balance: alertBalancePoints })

export interface BillGrantRow { id: number; amount: number; remark: string; created_at: number }
export interface BillDetailRow {
  day: string; model_name: string; cost_center_id: number | null; cost_center_name: string
  requests: number; cache_hits: number; prompt_tokens: number; completion_tokens: number; cost: number
}
export interface BillStatement {
  month: string; timezone: string; start_unix: number; end_unix: number
  /** 账期口径快照（1 元 = N 点）；历史月无快照时后端回退当前值 */
  points_per_yuan: number
  opening_limit: number | null; opening_used: number | null
  total_granted: number; total_revoked: number; consumption: number
  closing_limit: number; closing_used: number; closing_is_live: boolean
  chain_ok: boolean | null; no_usage_count: number; opening_missing: boolean
  revokes: BillGrantRow[]; grants: BillGrantRow[]
  rows: BillDetailRow[]; total_rows: number; total_cost_sum: number
}

// 三段式对账单（勾稽/冲减/明细）
export const apiOrgBillingStatement = (month: string, limit = 500) =>
  http.get<any, BillStatement>('/api/org/billing/statement', { params: { month, limit } })

// 对账单 CSV 下载（带 JWT 拉 blob）
export async function downloadOrgStatementCSV(month: string, orgName: string) {
  const resp = await http.get('/api/org/billing/statement/csv', {
    params: { month }, responseType: 'blob' as any,
  })
  const blob = resp as unknown as Blob
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = `对账单_${orgName}_${month}.csv`
  a.click()
  URL.revokeObjectURL(a.href)
}

// 子账号额度下发流水（配额下发页审计段；org 隔离，只含本客户子账号）
export const apiOrgQuotaGrants = (params?: any) => http.get<any, any>('/api/org/quota-grants', { params })
