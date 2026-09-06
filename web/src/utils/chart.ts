// 趋势图 option 工厂：全部单轴单系列（标题即系列名，无需图例）
// 数据系列色 #12A462 已通过 dataviz 六项校验（白表面，3.22:1）
const SERIES = '#12A462'
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

/** 两个单轴面板：请求趋势 / 成本趋势（token） */
export function trendOptions(points: DayPoint[]) {
  const dates = points.map((p) => p.date.slice(5))
  const req = baseOption(dates)
  req.series = [lineSeries('请求数', points.map((p) => p.requests), ' 次')]
  const cost = baseOption(dates)
  cost.series = [lineSeries('成本', points.map((p) => p.cost), ' token')]
  return { reqOption: req, costOption: cost }
}
