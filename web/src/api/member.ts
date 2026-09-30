import http from './http'

export interface MyKey {
  id: number
  name: string
  key_prefix: string
  status: number
  last_used_at: number | null
  created_at: number
  expired_at: number | null
  cost_center_id: number | null
  cost_center_name?: string
}

export interface MyModel {
  name: string
  display_name: string
  vendor: string
  input_price: number
  output_price: number
}

// 申请单双形态：kind=quota 额度申请（amount 生效）/ kind=model 模型授权申请（model_names 逗号分隔）
export interface QuotaRequestMine {
  id: number
  kind: 'quota' | 'model' | ''
  amount: number
  model_names: string
  reason: string
  status: 'pending' | 'approved' | 'rejected'
  reply: string
  created_at: number
}

// 可申请的模型（全部启用模型 + 本人是否已授权）
export interface AvailableModel {
  name: string
  display_name: string
  vendor: string
  granted: boolean
}

export const apiMyKeys = () => http.get<any, MyKey[]>('/api/member/keys')
export const apiCreateKey = (name: string, expiresAt?: number | null) =>
  http.post<any, any>('/api/member/keys', { name, expires_at: expiresAt ?? null })
export const apiDeleteKey = (id: number) => http.delete<any, any>(`/api/member/keys/${id}`)
export const apiMyModels = () => http.get<any, any>('/api/member/models')
export const apiMyStats = () => http.get<any, any>('/api/member/stats/overview')
// 多维用量统计：start/end 为 unix 秒闭开区间，缺省 = 当月（账期时区）
export const apiMyUsageBreakdown = (params?: { start?: number; end?: number }) =>
  http.get<any, any>('/api/member/stats/usage', { params })
export const apiMyUsage = (params?: any) => http.get<any, any>('/api/member/usage', { params })
export const apiMyRequests = () => http.get<any, QuotaRequestMine[]>('/api/member/requests')
// amount 走额度申请；models 非空走模型授权申请（两者互斥，后端同样校验）
export const apiCreateRequest = (payload: { amount?: number; models?: string[]; reason: string }) =>
  http.post<any, any>('/api/member/requests', payload)
export const apiAvailableModels = () => http.get<any, AvailableModel[]>('/api/member/models/available')
