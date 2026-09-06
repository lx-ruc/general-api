import dayjs from 'dayjs'

// token 数显示：千分位
export function fmtPoints(p: number | null | undefined): string {
  if (p == null) return '-'
  return p.toLocaleString('zh-CN')
}

// 额度/成本显示：以 token 为单位（≥1 百万时以「百万token」计）
// 说明：额度按 ¥1/百万token 折算为 token 预算（1 元 = 1,000,000 token），
// 高价模型（如 ¥2/百万token 输入）按单价等比多扣
export function fmtQuota(p: number | null | undefined): string {
  if (p == null) return '-'
  if (Math.abs(p) >= 1_000_000) {
    const m = p / 1_000_000
    const s = Number.isInteger(m) ? m.toString() : m.toFixed(2).replace(/\.?0+$/, '')
    return `${s} 百万token`
  }
  return `${p.toLocaleString('zh-CN')} token`
}

// token 数 → 元（默认 1 元 = 1,000,000 token 额度）
export function pointsToYuan(p: number | null | undefined, ppy = 1_000_000): string {
  if (p == null) return '-'
  return (p / ppy).toFixed(4)
}

// 单价 → 元/百万token
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
