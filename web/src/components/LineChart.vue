<script setup lang="ts">
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { LineChart, BarChart } from 'echarts/charts'
import { GridComponent, TooltipComponent } from 'echarts/components'
import VChart from 'vue-echarts'

use([CanvasRenderer, LineChart, BarChart, GridComponent, TooltipComponent])

withDefaults(defineProps<{ option: any; height?: string }>(), { height: '260px' })
</script>

<template>
  <VChart :option="option" autoresize :style="{ height }" />
</template>
