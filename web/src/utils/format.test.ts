import { describe, it, expect } from 'vitest'
import dayjs from 'dayjs'
import {
  roleLabel,
  yuanToPoints,
  pointsToYuan,
  fmtTokenCompact,
  fmtPrice1K,
  yuan1KToPoints,
  pointsToYuan1K,
  fmtYuan,
  fmtTime,
  fmtDate,
  fmtNum,
  roleNames,
} from './format'

describe('yuanToPoints / pointsToYuan：录入口元 ↔ 存储点数换算', () => {
  it('默认 1 元 = 1,000,000 点', () => {
    expect(yuanToPoints(100)).toBe(100_000_000)
    expect(yuanToPoints(0.01)).toBe(10_000)
    expect(yuanToPoints(0)).toBe(0)
    expect(pointsToYuan(100_000_000)).toBe(100)
    expect(pointsToYuan(2_500_000)).toBe(2.5)
    expect(pointsToYuan(0)).toBe(0)
  })

  it('元 → 点四舍五入取整点', () => {
    expect(yuanToPoints(0.000001)).toBe(1)
    expect(yuanToPoints(0.0000004)).toBe(0)
  })

  it('ppy 可调（points_per_yuan 配置）', () => {
    expect(yuanToPoints(100, 500_000)).toBe(50_000_000)
    expect(pointsToYuan(50_000_000, 500_000)).toBe(100)
  })

  it('负数（冲减/回收场景）对称换算', () => {
    expect(yuanToPoints(-2.5)).toBe(-2_500_000)
    expect(pointsToYuan(-2_500_000)).toBe(-2.5)
  })

  it('元 → 点 → 元往返一致（两位小数精度内）', () => {
    for (const yuan of [0.01, 1.99, 100, 12345.67]) {
      expect(pointsToYuan(yuanToPoints(yuan))).toBeCloseTo(yuan, 10)
    }
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

describe('fmtPrice1K：单价折算（元/1K token）', () => {
  it('默认 1 元 = 1,000,000 点，千 token 口径保留四位小数', () => {
    expect(fmtPrice1K(2_000_000)).toBe('¥0.0020')
    expect(fmtPrice1K(20_000_000)).toBe('¥0.0200')
    expect(fmtPrice1K(500_000)).toBe('¥0.0005')
    expect(fmtPrice1K(0)).toBe('¥0.0000')
  })

  it('ppy 可调（points_per_yuan 配置）', () => {
    expect(fmtPrice1K(1_000_000, 500_000)).toBe('¥0.0020')
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

describe('yuan1KToPoints / pointsToYuan1K：定价表单（元/1K token）↔点换算', () => {
  it('常见 1K 元价换算为整点（¥2/M = ¥0.002/1K）', () => {
    expect(yuan1KToPoints(0.002)).toBe(2_000_000)
    expect(yuan1KToPoints(0.02)).toBe(20_000_000)
    expect(yuan1KToPoints(0.0005)).toBe(500_000)
    expect(yuan1KToPoints(0)).toBe(0)
  })

  it('小数 1K 元价四舍五入取整点', () => {
    expect(yuan1KToPoints(0.00007)).toBe(70_000) // ¥0.07/M
    expect(yuan1KToPoints(0.0000000004)).toBe(0) // 不足 1 点归零
  })

  it('点→元/1K 回显与元/1K→点往返一致', () => {
    expect(pointsToYuan1K(2_000_000)).toBe(0.002)
    expect(pointsToYuan1K(20_000_000)).toBe(0.02)
    expect(pointsToYuan1K(500_000)).toBe(0.0005)
    for (const yuan of [0.002, 0.02, 0.00007, 0.015]) {
      expect(pointsToYuan1K(yuan1KToPoints(yuan))).toBeCloseTo(yuan, 10)
    }
  })
})

describe('fmtYuan：消耗点数 → 账单费用（元）', () => {
  it('整元/常规金额保留两位小数', () => {
    expect(fmtYuan(0)).toBe('¥0')
    expect(fmtYuan(1_000_000)).toBe('¥1.00')
    expect(fmtYuan(123_456_789)).toBe('¥123.46')
  })

  it('不足 1 元保留至多 4 位小数（尾零剥除）', () => {
    expect(fmtYuan(2_000)).toBe('¥0.002')
    expect(fmtYuan(500_000)).toBe('¥0.5')
    expect(fmtYuan(123_400)).toBe('¥0.1234')
  })

  it('负数（理论不出现，防御性）对称', () => {
    expect(fmtYuan(-2_000_000)).toBe('¥-2.00')
  })
})

describe('roleLabel：角色展示名（平台层超管分层）', () => {
  it('platform_admin 按 is_super 区分超级管理员/子管理员', () => {
    expect(roleLabel({ role: 'platform_admin', is_super: true })).toBe('超级管理员')
    expect(roleLabel({ role: 'platform_admin', is_super: false })).toBe('子管理员')
    expect(roleLabel({ role: 'platform_admin' })).toBe('子管理员') // is_super 缺省按子管理员
  })

  it('其余角色走 roleNames；空值兜底', () => {
    expect(roleLabel({ role: 'org_admin' })).toBe('客户管理员')
    expect(roleLabel({ role: 'member' })).toBe('子账号')
    expect(roleLabel(null)).toBe('')
    expect(roleLabel({})).toBe('')
  })
})
