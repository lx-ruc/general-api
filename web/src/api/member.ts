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

export interface MyCostCenter {
  id: number
  name: string
  status: number
}

export interface MyModel {
  name: string
  display_name: string
  vendor: string
  input_price: number
  output_price: number
}

export interface QuotaRequestMine {
  id: number
  amount: number
  reason: string
  status: 'pending' | 'approved' | 'rejected'
  reply: string
  created_at: number
}

export const apiMyKeys = () => http.get<any, MyKey[]>('/api/member/keys')
export const apiCreateKey = (name: string, expiresAt?: number | null, costCenterId?: number | null) =>
  http.post<any, any>('/api/member/keys', {
    name, expires_at: expiresAt ?? null, cost_center_id: costCenterId ?? null,
  })
export const apiDeleteKey = (id: number) => http.delete<any, any>(`/api/member/keys/${id}`)
export const apiMyCostCenters = () => http.get<any, MyCostCenter[]>('/api/member/cost-centers')
export const apiAssignKeyCenter = (id: number, costCenterId: number | null) =>
  http.put<any, any>(`/api/member/keys/${id}/cost-center`, { cost_center_id: costCenterId })
export const apiMyModels = () => http.get<any, any>('/api/member/models')
export const apiMyStats = () => http.get<any, any>('/api/member/stats/overview')
export const apiMyUsage = (params?: any) => http.get<any, any>('/api/member/usage', { params })
export const apiMyRequests = () => http.get<any, QuotaRequestMine[]>('/api/member/requests')
export const apiCreateRequest = (amount: number, reason: string) =>
  http.post<any, any>('/api/member/requests', { amount, reason })
