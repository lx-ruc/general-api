import http from './http'

export interface Member {
  id: number
  org_id: number
  username: string
  display_name: string
  role: string
  quota_limit: number | null
  quota_used: number
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
  last_used_at: number | null
  created_at: number
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

export const apiOrgRequests = (params?: any) => http.get<any, any>('/api/org/requests', { params })
export const apiHandleRequest = (id: number, action: 'approve' | 'reject', reply: string) =>
  http.put<any, any>(`/api/org/requests/${id}`, { action, reply })

// ---- 充值 / 账单 ----
export const apiOrgRecharges = () => http.get<any, any>('/api/org/recharges')
export const apiCreateRecharge = (amount: number, voucher: string) =>
  http.post<any, any>('/api/org/recharges', { amount, voucher })
export const apiOrgBankInfo = () => http.get<any, any>('/api/org/bank-info')
export const apiOrgBilling = (month: string) => http.get<any, any>('/api/org/billing', { params: { month } })
