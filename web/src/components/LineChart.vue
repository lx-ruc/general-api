<script setup lang="ts">
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent } from 'echarts/components'
import VChart from 'vue-echarts'

use([CanvasRenderer, LineChart, GridComponent, TooltipComponent])

defineProps<{ option: any }>()
</script>

<template>
  <VChart :option="option" autoresize style="height: 260px" />
</template>
