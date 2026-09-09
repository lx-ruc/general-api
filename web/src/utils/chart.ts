// 趋势图 option 工厂：全部单轴单系列（标题即系列名，无需图例）
// 数据系列色 #12A462 已通过 dataviz 六项校验（白表面，3.22:1）
const SERIES = '#12A462'
const INK = '#1b2b24'
const GRAPHITE = '#5c6b65'
const MUTED = '#8da098'
const GRID = '#e4ece7'
const AXIS = '#c8d6cf'

export interface DayPoint {
  date: string
  requests: number
  tokens: number
  cost: number
}

function baseOption(dates: string[]) {
  return {
    grid: { left: 44, right: 14, top: 26, bottom: 26 },
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'line', lineStyle: { color: AXIS, width: 1 } },
      backgroundColor: '#ffffff',
      borderColor: GRID,
      textStyle: { color: '#182420', fontSize: 12 },
      extraCssText: 'box-shadow: 0 4px 16px rgba(24,36,32,0.10); border-radius: 6px;',
    },
    xAxis: {
      type: 'category',
      data: dates,
      axisLine: { lineStyle: { color: AXIS } },
      axisTick: { show: false },
      axisLabel: { color: MUTED, fontSize: 11 },
    },
    yAxis: {
      type: 'value',
      splitLine: { lineStyle: { color: GRID } },
      axisLabel: { color: MUTED, fontSize: 11 },
    },
    series: [] as any[],
  }
}

function lineSeries(name: string, data: number[], unit = '') {
  return {
    name,
    type: 'line',
    data,
    lineStyle: { width: 2.5, color: SERIES },
    itemStyle: { color: SERIES },
    showSymbol: false,
    symbol: 'circle',
    symbolSize: 8,
    emphasis: { focus: 'series' },
    tooltip: { valueFormatter: (v: number) => `${v.toLocaleString('en-US')}${unit}` },
  }
}

/** 三个单轴面板：请求趋势 / tokens 趋势 / 额度消耗趋势（token） */
export function trendOptions(points: DayPoint[]) {
  const dates = points.map((p) => p.date.slice(5))
  const req = baseOption(dates)
  req.series = [lineSeries('请求数', points.map((p) => p.requests), ' 次')]
  const tok = baseOption(dates)
  tok.series = [lineSeries('tokens', points.map((p) => p.tokens), '')]
  const cost = baseOption(dates)
  cost.series = [lineSeries('额度消耗', points.map((p) => p.cost), ' token')]
  return { reqOption: req, tokensOption: tok, costOption: cost }
}

/**
 * 横向条形图（分类构成对比，如 各模型成本 / 客户消耗 Top）
 * 单一绿色（身份由 y 轴标签承载，不做彩色编码）；条端 4px 圆角贴基线；
 * 值直接标注在条端。入参按"最大在前"排序，内部反转使最大者显示在顶部。
 */
export function barOption(categories: string[], values: number[], unit = ' token') {
  const cats = [...categories].reverse()
  const vals = [...values].reverse()
  return {
    grid: { left: 8, right: 64, top: 6, bottom: 6, containLabel: true },
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'shadow', shadowStyle: { color: 'rgba(18,164,98,0.06)' } },
      backgroundColor: '#ffffff',
      borderColor: GRID,
      textStyle: { color: INK, fontSize: 12 },
      extraCssText: 'box-shadow: 0 4px 16px rgba(24,36,32,0.10); border-radius: 6px;',
      valueFormatter: (v: number) => `${v.toLocaleString('en-US')}${unit}`,
    },
    xAxis: {
      type: 'value',
      splitLine: { lineStyle: { color: GRID } },
      axisLabel: { color: MUTED, fontSize: 11 },
    },
    yAxis: {
      type: 'category',
      data: cats,
      axisLine: { show: false },
      axisTick: { show: false },
      axisLabel: { color: GRAPHITE, fontSize: 12, width: 120, overflow: 'truncate' },
    },
    series: [
      {
        type: 'bar',
        data: vals,
        barWidth: 14,
        itemStyle: { color: SERIES, borderRadius: [0, 4, 4, 0] },
        label: {
          show: true,
          position: 'right',
          color: GRAPHITE,
          fontSize: 11,
          formatter: (p: { value: number }) =>
            p.value > 0 ? p.value.toLocaleString('en-US') : '',
        },
      },
    ],
  }
}
