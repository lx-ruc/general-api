import http from './http'

// ---- 公司 ----
export interface Org {
  id: number
  name: string
  remark: string
  quota_limit: number
  quota_used: number
  status: number
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
  weight: number
  priority: number
  status: number
  last_test_at: number | null
  last_test_ok: number
  remark: string
  models?: ChannelAbility[]
}

export const apiListChannels = () => http.get<any, Channel[]>('/api/platform/channels')
export const apiCreateChannel = (data: any) => http.post<any, any>('/api/platform/channels', data)
export const apiGetChannel = (id: number) => http.get<any, any>(`/api/platform/channels/${id}`)
export const apiUpdateChannel = (id: number, data: any) => http.put<any, any>(`/api/platform/channels/${id}`, data)
export const apiUpdateChannelStatus = (id: number, status: number) =>
  http.put<any, any>(`/api/platform/channels/${id}/status`, { status })
export const apiDeleteChannel = (id: number) => http.delete<any, any>(`/api/platform/channels/${id}`)
export const apiTestChannel = (id: number) => http.post<any, any>(`/api/platform/channels/${id}/test`)

// ---- 模型 ----
export interface MModel {
  id: number
  name: string
  display_name: string
  vendor: string
  input_price: number
  output_price: number
  status: number
  remark: string
}

export const apiListModels = () => http.get<any, MModel[]>('/api/platform/models')
export const apiCreateModel = (data: any) => http.post<any, any>('/api/platform/models', data)
export const apiUpdateModel = (id: number, data: any) => http.put<any, any>(`/api/platform/models/${id}`, data)
export const apiDeleteModel = (id: number) => http.delete<any, any>(`/api/platform/models/${id}`)

// ---- 统计 / 日志 ----
export const apiStatsOverview = () => http.get<any, any>('/api/platform/stats/overview')
export const apiListUsage = (params?: any) => http.get<any, any>('/api/platform/usage', { params })
