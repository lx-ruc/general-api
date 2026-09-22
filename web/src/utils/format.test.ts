import { describe, it, expect } from 'vitest'
import dayjs from 'dayjs'
import {
  fmtQuota,
  fmtTokenCompact,
  fmtPrice,
  fmtPrice1K,
  yuanToPoints,
  pointsToYuan,
  fmtTime,
  fmtDate,
  fmtNum,
  roleNames,
} from './format'

describe('fmtQuota：额度以 token 口径展示', () => {
  it('空值（null/undefined）返回 -', () => {
    expect(fmtQuota(null)).toBe('-')
    expect(fmtQuota(undefined)).toBe('-')
  })

  it('百万以下原样带千分位', () => {
    expect(fmtQuota(0)).toBe('0 token')
    expect(fmtQuota(999_999)).toBe('999,999 token')
  })

  it('百万及以上折算 M token，去尾零', () => {
    expect(fmtQuota(1_000_000)).toBe('1M token')
    expect(fmtQuota(2_000_000)).toBe('2M token')
    expect(fmtQuota(2_500_000)).toBe('2.5M token')
    expect(fmtQuota(1_100_000)).toBe('1.1M token')
    expect(fmtQuota(10_000_000)).toBe('10M token')
  })

  it('负额度同样折算（欠费/冲减场景）', () => {
    expect(fmtQuota(-2_500_000)).toBe('-2.5M token')
  })
})

describe('fmtTokenCompact：token 紧凑单位 k→M→G→T', () => {
  it('空值返回 -，千以下原样', () => {
    expect(fmtTokenCompact(null)).toBe('-')
    expect(fmtTokenCompact(undefined)).toBe('-')
    expect(fmtTokenCompact(0)).toBe('0')
    expect(fmtTokenCompact(999)).toBe('999')
  })

  it('k / M / G / T 各档与有效数字位数', () => {
    expect(fmtTokenCompact(1_000)).toBe('1k')
    expect(fmtTokenCompact(1_500)).toBe('1.5k')
    expect(fmtTokenCompact(12_345)).toBe('12.3k')
    expect(fmtTokenCompact(123_456)).toBe('123k')
    expect(fmtTokenCompact(1_234_567)).toBe('1.23M')
    expect(fmtTokenCompact(12_345_678)).toBe('12.3M')
    expect(fmtTokenCompact(123_456_789)).toBe('123M')
    expect(fmtTokenCompact(1_000_000_000)).toBe('1G')
    expect(fmtTokenCompact(1_000_000_000_000)).toBe('1T')
  })

  it('四舍五入顶到 1000 时进一档（999,999 → 1M 而非 1000k）', () => {
    expect(fmtTokenCompact(999_999)).toBe('1M')
    expect(fmtTokenCompact(-999_999)).toBe('-1M')
  })

  it('整尾零不被误剥（200k 曾显示成 2k，150k 显示成 15k）', () => {
    expect(fmtTokenCompact(150_000)).toBe('150k')
    expect(fmtTokenCompact(200_000)).toBe('200k')
    expect(fmtTokenCompact(300_000)).toBe('300k')
    expect(fmtTokenCompact(990_000)).toBe('990k')
    expect(fmtTokenCompact(1_200_000)).toBe('1.2M')
  })

  it('T 档封顶不再进位', () => {
    expect(fmtTokenCompact(1e15)).toBe('1000T')
  })

  it('负数保留符号', () => {
    expect(fmtTokenCompact(-1_500)).toBe('-1.5k')
  })
})

describe('fmtPrice / fmtPrice1K：单价折算', () => {
  it('默认 1 元 = 1,000,000 点，保留两位小数', () => {
    expect(fmtPrice(2_000_000)).toBe('¥2.00')
    expect(fmtPrice(500_000)).toBe('¥0.50')
    expect(fmtPrice(250_000)).toBe('¥0.25')
    expect(fmtPrice(0)).toBe('¥0.00')
  })

  it('ppy 可调（points_per_yuan 配置）', () => {
    expect(fmtPrice(1_000_000, 500_000)).toBe('¥2.00')
  })

  it('千 token 口径保留四位小数', () => {
    expect(fmtPrice1K(2_000_000)).toBe('¥0.0020')
    expect(fmtPrice1K(20_000_000)).toBe('¥0.0200')
  })
})

describe('fmtTime / fmtDate / fmtNum', () => {
  it('空值与 0 返回 -', () => {
    expect(fmtTime(null)).toBe('-')
    expect(fmtTime(0)).toBe('-')
    expect(fmtDate(null)).toBe('-')
    expect(fmtDate(0)).toBe('-')
  })

  it('时间格式与 dayjs 本地时区一致（跨时区可复现）', () => {
    const ts = 1_731_000_000
    expect(fmtTime(ts)).toBe(dayjs.unix(ts).format('YYYY-MM-DD HH:mm:ss'))
    expect(fmtTime(ts)).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/)
    expect(fmtDate(ts)).toBe(dayjs.unix(ts).format('YYYY-MM-DD'))
  })

  it('fmtNum：空值归 0，千分位', () => {
    expect(fmtNum(null)).toBe('0')
    expect(fmtNum(undefined)).toBe('0')
    expect(fmtNum(0)).toBe('0')
    expect(fmtNum(1_234_567)).toBe('1,234,567')
    expect(fmtNum(-9_876)).toBe('-9,876')
  })

  it('roleNames 三角色齐备', () => {
    expect(roleNames.platform_admin).toBe('系统管理员')
    expect(roleNames.org_admin).toBe('客户管理员')
    expect(roleNames.member).toBe('子账号')
  })
})

describe('yuanToPoints / pointsToYuan：定价表单元↔点换算', () => {
  it('常见元价换算为整点', () => {
    expect(yuanToPoints(3)).toBe(3_000_000)
    expect(yuanToPoints(0.5)).toBe(500_000)
    expect(yuanToPoints(0)).toBe(0)
  })

  it('小数元价四舍五入取整点', () => {
    expect(yuanToPoints(0.07)).toBe(70_000)
    expect(yuanToPoints(0.0000004)).toBe(0) // 不足 1 点归零
  })

  it('点→元回显与元→点往返一致', () => {
    expect(pointsToYuan(2_000_000)).toBe(2)
    expect(pointsToYuan(500_000)).toBe(0.5)
    for (const yuan of [3, 0.5, 0.07, 15]) {
      expect(pointsToYuan(yuanToPoints(yuan))).toBeCloseTo(yuan, 10)
    }
  })
})
