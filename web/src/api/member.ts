import http from './http'

export interface MyKey {
  id: number
  name: string
  key_prefix: string
  status: number
  last_used_at: number | null
  created_at: number
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
export const apiCreateKey = (name: string) => http.post<any, any>('/api/member/keys', { name })
export const apiDeleteKey = (id: number) => http.delete<any, any>(`/api/member/keys/${id}`)
export const apiMyModels = () => http.get<any, any>('/api/member/models')
export const apiMyStats = () => http.get<any, any>('/api/member/stats/overview')
export const apiMyUsage = (params?: any) => http.get<any, any>('/api/member/usage', { params })
export const apiMyRequests = () => http.get<any, QuotaRequestMine[]>('/api/member/requests')
export const apiCreateRequest = (amount: number, reason: string) =>
  http.post<any, any>('/api/member/requests', { amount, reason })
