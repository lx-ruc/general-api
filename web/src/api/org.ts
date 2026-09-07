import http from './http'

export interface Member {
  id: number
  org_id: number
  username: string
  display_name: string
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

export interface QuotaRequestRow {
  id: number
  org_id: number
  user_id: number
  username?: string
  amount: number
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

// ---- 成本中心 ----
export const apiOrgCostCenters = () => http.get<any, { list: CostCenter[]; require_cost_center: number }>('/api/org/cost-centers')
export const apiCreateCostCenter = (name: string) => http.post<any, any>('/api/org/cost-centers', { name })
export const apiUpdateCostCenter = (id: number, data: { name?: string; status?: number }) =>
  http.put<any, any>(`/api/org/cost-centers/${id}`, data)
export const apiUpdateCostCenterConfig = (require_cost_center: number) =>
  http.put<any, any>('/api/org/cost-centers/config', { require_cost_center })
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

// ---- 充值 / 账单 ----
export const apiOrgRecharges = () => http.get<any, any>('/api/org/recharges')
export const apiCreateRecharge = (amount: number, voucher: string) =>
  http.post<any, any>('/api/org/recharges', { amount, voucher })
export const apiOrgBankInfo = () => http.get<any, any>('/api/org/bank-info')
export const apiOrgBilling = (month: string) => http.get<any, any>('/api/org/billing', { params: { month } })

// 额度预警：读取本公司阈值与状态
export const apiGetAlertLevels = () => http.get<any, any>('/api/org/alert-levels')
// 额度预警：设置阈值（0 = 关闭）
export const apiUpdateAlertLevels = (threshold: number) =>
  http.put<any, any>('/api/org/alert-levels', { threshold })

export interface BillGrantRow { id: number; amount: number; remark: string; created_at: number }
export interface BillDetailRow {
  day: string; model_name: string; cost_center_id: number | null; cost_center_name: string
  requests: number; cache_hits: number; prompt_tokens: number; completion_tokens: number; cost: number
}
export interface BillStatement {
  month: string; timezone: string; start_unix: number; end_unix: number
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
