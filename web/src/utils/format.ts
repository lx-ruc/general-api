import dayjs from 'dayjs'

// 用量 tokens 显示：与额度折元前的 token 口径一致（≥1 百万以「M」计，M=百万；小值保持精确）。
// 只用于真实 token 数列；额度/消耗/流水/余额一律 fmtYuan（元口径）
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

// 单价 → 元/1K token（需求规格 4.5 口径；存储仍为点/百万token，展示时折算）
export function fmtPrice1K(price: number, ppy = 1_000_000): string {
  return `¥${(price / ppy / 1000).toFixed(4)}`
}

// 元/1K → 存储点数（¥1/1K = ¥1000/M = 1000×ppy 点）；定价表单按元/1K 输入，入库前换算，四舍五入取整点
export function yuan1KToPoints(yuan1k: number, ppy = 1_000_000): number {
  return Math.round(yuan1k * 1000 * ppy)
}

// 存储点数 → 元/1K（表单回显用）
export function pointsToYuan1K(points: number, ppy = 1_000_000): number {
  return points / ppy / 1000
}

// 消耗点数 → 账单费用（元）：1 元 = ppy 点（默认 100 万点）。
// ≥1 元保留两位小数；不足 1 元保留至多 4 位（避免小额被抹成 ¥0.00）
export function fmtYuan(points: number, ppy = 1_000_000): string {
  const yuan = points / ppy
  if (Math.abs(yuan) >= 1) return `¥${yuan.toFixed(2)}`
  return `¥${yuan.toFixed(4).replace(/\.?0+$/, '')}`
}

// 元 → 存储点数（表单提交前换算，四舍五入取整点；额度录入统一按元输入）
export function yuanToPoints(yuan: number, ppy = 1_000_000): number {
  return Math.round(yuan * ppy)
}

// 存储点数 → 元（表单回显用）
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
