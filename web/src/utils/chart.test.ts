import { describe, it, expect } from 'vitest'
import { trendOptions, barOption, type DayPoint } from './chart'

const points: DayPoint[] = [
  { date: '2026-09-01', requests: 10, tokens: 1_100, cost: 2_200 },
  { date: '2026-09-02', requests: 40, tokens: 4_400, cost: 8_800 },
  { date: '2026-09-03', requests: 25, tokens: 2_500, cost: 5_000 },
]

describe('trendOptions：三面板趋势图', () => {
  it('日期轴取 MM-DD，三面板各取各的系列值', () => {
    const { reqOption, tokensOption, costOption } = trendOptions(points)
    expect(reqOption.xAxis.data).toEqual(['09-01', '09-02', '09-03'])
    expect(reqOption.series[0].data).toEqual([10, 40, 25])
    expect(tokensOption.series[0].data).toEqual([1_100, 4_400, 2_500])
    expect(costOption.series[0].data).toEqual([2_200, 8_800, 5_000])
  })

  it('三个面板均为单系列折线，系列名即标题', () => {
    const { reqOption, tokensOption, costOption } = trendOptions(points)
    expect(reqOption.series).toHaveLength(1)
    expect(reqOption.series[0]).toMatchObject({ name: '请求数', type: 'line' })
    expect(tokensOption.series[0]).toMatchObject({ name: 'tokens', type: 'line' })
    expect(costOption.series[0]).toMatchObject({ name: '额度消耗', type: 'line' })
  })

  it('tooltip 数值带各自单位（千分位 + 后缀）', () => {
    const { reqOption, tokensOption, costOption } = trendOptions(points)
    expect(reqOption.series[0].tooltip.valueFormatter(1_234)).toBe('1,234 次')
    expect(tokensOption.series[0].tooltip.valueFormatter(5_000)).toBe('5,000')
    expect(costOption.series[0].tooltip.valueFormatter(42)).toBe('42 token')
  })

  it('空数据不抛异常（空看板冷启动）', () => {
    const { reqOption } = trendOptions([])
    expect(reqOption.xAxis.data).toEqual([])
    expect(reqOption.series[0].data).toEqual([])
  })
})

describe('barOption：横向条形图', () => {
  it('入参最大在前，内部反转使最大者显示在顶部', () => {
    const opt = barOption(['模型A', '模型B', '模型C'], [3, 1, 2])
    expect(opt.yAxis.data).toEqual(['模型C', '模型B', '模型A'])
    expect(opt.series[0].data).toEqual([2, 1, 3])
    expect(opt.series[0].type).toBe('bar')
  })

  it('不改动调用方数组（浅拷贝后反转）', () => {
    const cats = ['a', 'b', 'c']
    const vals = [3, 1, 2]
    barOption(cats, vals)
    expect(cats).toEqual(['a', 'b', 'c'])
    expect(vals).toEqual([3, 1, 2])
  })

  it('条端标注：正值显示千分位，0 与负值隐藏', () => {
    const fmt = barOption(['a'], [1]).series[0].label.formatter
    expect(fmt({ value: 42 })).toBe('42')
    expect(fmt({ value: 1_234_567 })).toBe('1,234,567')
    expect(fmt({ value: 0 })).toBe('')
    expect(fmt({ value: -5 })).toBe('')
  })

  it('tooltip 默认单位 token，可自定义', () => {
    expect(barOption(['a'], [1]).tooltip.valueFormatter(1_000)).toBe('1,000 token')
    expect(barOption(['a'], [1], ' 次').tooltip.valueFormatter(1_000)).toBe('1,000 次')
  })
})
