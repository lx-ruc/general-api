import dayjs from 'dayjs'

// 额度/消耗显示：以 token 为单位（≥1 百万时以「M token」计，M=百万）
// 说明：额度即 token 预算，高价模型按单价等比多扣
export function fmtQuota(p: number | null | undefined): string {
  if (p == null) return '-'
  if (Math.abs(p) >= 1_000_000) {
    const m = p / 1_000_000
    const s = Number.isInteger(m) ? m.toString() : m.toFixed(2).replace(/\.?0+$/, '')
    return `${s}M token`
  }
  return `${p.toLocaleString('zh-CN')} token`
}

// 用量 tokens 显示：与 fmtQuota 同 M 口径（≥1 百万以「M」计，M=百万；小值保持精确）
export function fmtTokensM(n: number | null | undefined): string {
  if (n == null) return '-'
  if (Math.abs(n) >= 1_000_000) {
    const m = n / 1_000_000
    const s = Number.isInteger(m) ? m.toString() : m.toFixed(2).replace(/\.?0+$/, '')
    return `${s}M`
  }
  return n.toLocaleString('zh-CN')
}

// token 数紧凑单位：<1k 原样；之后 k → M → G → T 封顶（至多 3 位有效数字）
export function fmtTokenCompact(n: number | null | undefined): string {
  if (n == null) return '-'
  const abs = Math.abs(n)
  if (abs < 1_000) return n.toLocaleString('zh-CN')
  const units = ['k', 'M', 'G', 'T']
  const exp = Math.min(Math.floor(Math.log10(abs) / 3), units.length) // 1=k … 4=T
  const v = n / 10 ** (3 * exp)
  const s = Math.abs(v) >= 100 ? v.toFixed(0) : Math.abs(v) >= 10 ? v.toFixed(1) : v.toFixed(2)
  // 尾零只对小数部分有意义（'1.50'→'1.5'）；纯整数 '200' 不能剥成 '2'
  const stripTail = (x: string) => (x.includes('.') ? x.replace(/\.?0+$/, '') : x)
  // 四舍五入顶到 1000（如 999,999 → 1000k）时进一档
  if (Math.abs(Number(s)) >= 1_000 && exp < units.length) {
    return `${stripTail((Number(s) / 1_000).toString())}${units[exp]}`
  }
  return `${stripTail(s)}${units[exp - 1]}`
}

// 单价 → 元/M token
export function fmtPrice(price: number, ppy = 1_000_000): string {
  return `¥${(price / ppy).toFixed(2)}`
}

// 单价 → 元/千token（需求规格 4.5 口径）
export function fmtPrice1K(price: number, ppy = 1_000_000): string {
  return `¥${(price / ppy / 1000).toFixed(4)}`
}

// 元/M → 存储点数（1 元 = ppy 点）；定价表单按元输入，入库前换算，四舍五入取整点
export function yuanToPoints(yuan: number, ppy = 1_000_000): number {
  return Math.round(yuan * ppy)
}

// 存储点数 → 元/M（表单回显用）
export function pointsToYuan(points: number, ppy = 1_000_000): number {
  return points / ppy
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
  platform_admin: '系统管理员',
  org_admin: '客户管理员',
  member: '子账号',
}
