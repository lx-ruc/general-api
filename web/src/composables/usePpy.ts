import { useAuthStore } from '../stores/auth'

// 全站点/元汇率（1 元 = ppy 点）：视图里额度折元/元转点数的统一取用口。
// 返回的是 store 里的 ref（响应式）：/api/me 刷新后所有调用点自动跟随。
// 默认 100 万（store 初始值），fetchMe 成功后即为服务端配置值。
export function usePpy() {
  return useAuthStore().ppy
}
