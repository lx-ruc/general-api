import dayjs from 'dayjs'

// 点数显示：千分位
export function fmtPoints(p: number | null | undefined): string {
  if (p == null) return '-'
  return p.toLocaleString('zh-CN')
}

// 点数 → 元（默认 1 元 = 1,000,000 点）
export function pointsToYuan(p: number | null | undefined, ppy = 1_000_000): string {
  if (p == null) return '-'
  return (p / ppy).toFixed(4)
}

// 单价（点/1M token）→ 元/1M token
export function fmtPrice(price: number, ppy = 1_000_000): string {
  return `¥${(price / ppy).toFixed(2)}`
}

export function fmtTime(unix: number | null | undefined): string {
  if (!unix) return '-'
  return dayjs.unix(unix).format('YYYY-MM-DD HH:mm:ss')
}

export function fmtDate(unix: number | null | undefined): string {
  if (!unix) return '-'
  return dayjs.unix(unix).format('YYYY-MM-DD')
}

export function fmtNum(n: number | null | undefined): string {
  if (n == null) return '0'
  return n.toLocaleString('zh-CN')
}

export const roleNames: Record<string, string> = {
  platform_admin: '平台管理员',
  org_admin: '公司管理员',
  member: '员工',
}
