<script setup lang="ts">
// 无线网络面板：WiFi 信道扫描与评估、自动切换备选网络（白名单）、切换日志。
// 数据源 = agent 新能力 net_get.wifi_quality（iw 优先 / nmcli 降级）。
// 安全红线：只连接/切换白名单内 SSID；白名单密码仅下发写盘，不回显。
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import * as echarts from 'echarts'
import { Refresh, Switch, Aim, Connection } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api, ResultStatus, type ApiNode } from '@/api/client'

const props = defineProps<{ node: ApiNode }>()

// ---------------------------------------------------------------------------
// 类型
// ---------------------------------------------------------------------------
interface ApInfo { bssid: string; ssid: string; freq: number; channel: number; band: string; rssi: number; security: string; signal: number }
interface ChannelStat { channel: number; count: number; best_rssi: number; avg_rssi: number; congestion: number; score: number }
interface BandReport { band: string; channels: Record<number, ChannelStat>; best?: ChannelStat }
interface CurrentLink { ssid: string; bssid: string; freq: number; channel: number; band: string; rssi: number; signal: number; bitrate?: string }
interface Rec { kind: string; ssid?: string; channel?: number; band?: string; rssi?: number; reason: string }
interface SwitchLog { ts: string; event: string; from?: string; to?: string; ok: boolean; msg?: string }
interface GuardState { enabled: boolean; threshold: number; min_margin: number; interval_sec: number; check_gateway: string; whitelist: string[]; last_check?: string; last_switch?: string; log: SwitchLog[] }
interface AssessReport { interface: string; scanned_at: string; tool: string; current?: CurrentLink; ap_list: ApInfo[]; bands: Record<string, BandReport>; recommendations: Rec[]; guard: GuardState }

const loading = ref(false)
const report = ref<AssessReport | null>(null)
const error = ref('')

// 引擎配置表单（白名单编辑时密码留空 = 保持原密码）
const guardForm = ref<{ enabled: boolean; threshold: number; rows: { ssid: string; password: string }[] }>({
  enabled: false,
  threshold: -75,
  rows: [],
})

async function load(showSpin = true) {
  if (showSpin) loading.value = true
  try {
    const r = await api.execCommandAuto(props.node.id, 'net_get', { action: 'wifi_quality' }, 40)
    if (r.status === ResultStatus.FAILED) {
      error.value = r.message || r.stdout || '评估失败'
      ElMessage.error(error.value)
      return
    }
    error.value = ''
    const rep = JSON.parse(r.stdout || '{}') as AssessReport
    report.value = rep
    syncGuardForm(rep.guard)
    await nextTick()
    drawCharts()
  } catch (e: any) {
    error.value = e?.message || '评估失败'
    ElMessage.error(`获取信道评估失败：${error.value}`)
  } finally {
    loading.value = false
  }
}

function syncGuardForm(g?: GuardState) {
  guardForm.value = {
    enabled: !!g?.enabled,
    threshold: g?.threshold ?? -75,
    rows: (g?.whitelist ?? []).map((ssid) => ({ ssid, password: '' })),
  }
}

// —— 手动切换（白名单内，agent 会二次校验） ——
const switching = ref('')
async function switchTo(ssid: string) {
  await ElMessageBox.confirm(
    `将节点切换到 WiFi「${ssid}」；切换瞬间 SSH/Tailscale/业务可能有短暂中断（约 10-30 秒）。`,
    '确认切换',
    { confirmButtonText: '切换', cancelButtonText: '取消', type: 'warning' },
  ).catch(() => null)
  switching.value = ssid
  try {
    const r = await api.execCommandAuto(props.node.id, 'net_set', { action: 'wifi_switch', ssid }, 60)
    if (r.status === ResultStatus.NEEDS_PRIVILEGE) {
      ElMessageBox.alert(
        `<div style="font-size:13px"><p style="margin-bottom:8px">需要 root 权限，请在节点执行：</p><pre class="mono" style="white-space:pre-wrap;background:var(--el-fill-color-light);padding:10px;border-radius:6px">${r.privilege_script || ''}</pre></div>`,
        '需要提权',
        { dangerouslyUseHTMLString: true, confirmButtonText: '关闭' },
      ).catch(() => null)
    } else if (r.status === ResultStatus.FAILED) {
      ElMessage.error(`切换失败：${r.message || r.stdout || ''}`)
    } else {
      ElMessage.success(`已切换到 ${ssid}，稍候重新扫描验证`)
      setTimeout(load, 8000)
    }
  } catch (e: any) {
    ElMessage.error(`切换失败：${e?.message || ''}`)
  } finally {
    switching.value = ''
  }
}

// —— 保存引擎配置 ——
const saving = ref(false)
async function saveConfig() {
  const f = guardForm.value
  const rows = f.rows.filter((r) => r.ssid.trim())
  const cfg = {
    enabled: f.enabled,
    threshold: f.threshold,
    whitelist: rows.map((r) => ({ ssid: r.ssid.trim(), password: r.password })),
  }
  saving.value = true
  try {
    const r = await api.execCommandAuto(props.node.id, 'net_set', { action: 'wifi_guard_config', config: JSON.stringify(cfg) }, 30)
    if (r.status === ResultStatus.FAILED) {
      ElMessage.error(`保存失败：${r.message || r.stdout || ''}`)
      return
    }
    ElMessage.success('自动切换配置已保存并生效')
    setTimeout(load, 1500)
  } catch (e: any) {
    ElMessage.error(`保存失败：${e?.message || ''}`)
  } finally {
    saving.value = false
  }
}

function addRow() {
  guardForm.value.rows.push({ ssid: '', password: '' })
}
function removeRow(i: number) {
  guardForm.value.rows.splice(i, 1)
}

// ---------------------------------------------------------------------------
// ECharts 信道占用图
// ---------------------------------------------------------------------------
let chart24: echarts.ECharts | null = null
let chart5: echarts.ECharts | null = null
let chart6: echarts.ECharts | null = null

function drawCharts() {
  const rep = report.value
  if (!rep) return
  buildChart('chart-24', rep, '2.4')
  buildChart('chart-5', rep, '5')
  buildChart('chart-6', rep, '6')
}

function buildChart(elId: string, rep: AssessReport, band: string) {
  const el = document.getElementById(elId)
  if (!el) return
  const chart = echarts.init(el)
  const bandRep = rep.bands?.[band]
  const curCh = rep.current?.band === band ? rep.current.channel : 0

  const channels: number[] = []
  const counts: number[] = []
  const colors: string[] = []
  const scores: number[] = []
  const maxCh = band === '2.4' ? 13 : band === '5' ? 165 : 233
  const minCh = band === '2.4' ? 1 : band === '5' ? 36 : 1
  const step = band === '5' ? 4 : 2

  if (!bandRep || !bandRep.channels || Object.keys(bandRep.channels).length === 0) {
    el.innerHTML = ''
    return
  }
  const present = new Set(Object.keys(bandRep.channels).map(Number))
  for (let ch = minCh; ch <= maxCh; ch += step) {
    if (ch === 42 || ch === 50 || ch === 58 || ch === 114 || ch === 122 || ch === 130) continue // 跳过 DFS 禁区(部分)
    if (!present.has(ch) && band !== '2.4') continue
    const st = bandRep.channels[ch]
    channels.push(ch)
    counts.push(st?.count ?? 0)
    scores.push(st?.score ?? 0)
    colors.push(colorOf(st?.congestion ?? 0))
  }

  // 手动补齐 2.4G 全信道（无柱也显示轴）
  if (band === '2.4') {
    const full: number[] = []
    for (let ch = 1; ch <= 13; ch++) full.push(ch)
    for (const ch of full) {
      if (!channels.includes(ch)) {
        channels.push(ch)
        counts.push(0)
        scores.push(-100)
        colors.push('#e8e8e8')
      }
    }
    channels.sort((a, b) => a - b)
    const idx = (c: number) => channels.indexOf(c)
    counts.sort(() => 0)
    // 重新对排同步（简单方式：重建数组）
    const rebuilt = channels.map((ch) => {
      const st = bandRep.channels[ch]
      return {
        ch,
        count: st?.count ?? 0,
        color: st ? colorOf(st.congestion) : '#e8e8e8',
        score: st?.score ?? -100,
        cur: ch === curCh,
      }
    })
    channels.splice(0, channels.length, ...rebuilt.map((r) => r.ch))
    counts.splice(0, counts.length, ...rebuilt.map((r) => r.count))
    colors.splice(0, colors.length, ...rebuilt.map((r) => r.color))
    scores.splice(0, scores.length, ...rebuilt.map((r) => r.score))
    void idx
  }

  const mark = curCh ? [{ xAxis: curCh, label: { formatter: '当前', color: '#f56c6c' }, itemStyle: { color: '#f56c6c' } }] : []

  chart.setOption({
    tooltip: {
      trigger: 'axis',
      formatter: (ps: any[]) => {
        if (!ps?.length) return ''
        const p = ps[0]
        const st = bandRep.channels[p.axisValue]
        const curS = p.axisValue === curCh ? ' ⬤当前' : ''
        return [
          `<b>信道 ${p.axisValue}</b>${curS}`,
          `AP 数: ${st?.count ?? 0}`,
          `最强信号: ${st?.best_rssi ?? '-'} dBm`,
          `平均信号: ${st?.avg_rssi ?? '-'} dBm`,
          `综合评价: ${st?.score ?? '-'}`,
        ].join('<br/>')
      },
    },
    grid: { left: 34, right: 12, top: 26, bottom: 24 },
    xAxis: {
      type: 'category',
      data: channels,
      axisLabel: { fontSize: 10 },
      axisLine: { lineStyle: { color: 'rgba(120,120,120,.4)' } },
      axisTick: { show: false },
    },
    yAxis: {
      type: 'value',
      minInterval: 1,
      name: 'AP 数',
      nameTextStyle: { fontSize: 10 },
      axisLabel: { fontSize: 10 },
      splitLine: { lineStyle: { color: 'rgba(120,120,120,.12)' } },
    },
    series: [
      {
        name: 'AP 数',
        type: 'bar',
        barWidth: band === '2.4' ? 22 : 14,
        data: channels.map((ch) => ({ value: counts[channels.indexOf(ch)], itemStyle: { color: colors[channels.indexOf(ch)] } })),
        markPoint: {
          symbol: 'pin',
          symbolSize: 26,
          data: mark,
          label: { fontSize: 10 },
        },
      },
    ],
  })
  chart.on('click', (p: any) => {
    if (p?.axisValue) {
      const ch = Number(p.axisValue)
      const st = bandRep.channels[ch]
      if (st && st.count > 0) {
        ElMessage.info(`信道 ${ch}：${st.count} 个 AP，最优 ${st.best_rssi}dBm，评分 ${st.score}`)
      } else {
        ElMessage.info(`信道 ${ch}：目前空闲`)
      }
    }
  })
  const key = elId.replace('chart-', '')
  if (key === '24') {
    chart24?.dispose()
    chart24 = chart
  } else if (key === '5') {
    chart5?.dispose()
    chart5 = chart
  } else {
    chart6?.dispose()
    chart6 = chart
  }
}

function colorOf(congestion: number): string {
  if (congestion < 40) return '#67c23a'
  if (congestion < 70) return '#e6a23c'
  return '#f56c6c'
}

function rssiColor(rssi: number): string {
  if (rssi >= -60) return '#67c23a'
  if (rssi >= -75) return '#e6a23c'
  return '#f56c6c'
}
function bandLabel(b: string) {
  return { '2.4': '2.4 GHz', '5': '5 GHz', '6': '6 GHz' }[b] || b
}

function fmtTs(ts?: string) {
  if (!ts) return '-'
  const d = new Date(ts)
  return isNaN(d.getTime()) ? ts : d.toLocaleString('zh-CN', { hour12: false })
}

let timer = 0
onMounted(() => {
  load()
  timer = window.setInterval(() => load(false), 60000) // 每分钟自动刷新
})
onBeforeUnmount(() => {
  window.clearInterval(timer)
  chart24?.dispose()
  chart5?.dispose()
  chart6?.dispose()
})
</script>

<template>
  <div class="wifi-panel">
    <!-- 工具栏 -->
    <div style="display: flex; align-items: center; gap: 8px; margin-bottom: 2px">
      <el-icon><Connection /></el-icon>
      <span style="font-weight: 600">WiFi 信道评估</span>
      <span class="text-secondary" style="font-size: 12px">
        {{ report?.scanned_at ? `上次扫描 ${fmtTs(report.scanned_at)}` : '' }}
        · 数据源 {{ report?.tool === 'iw' ? 'iw（完整）' : report?.tool === 'nmcli' ? 'nmcli（精简）' : '-' }}
      </span>
      <span style="flex: 1"></span>
      <el-button size="small" :icon="Refresh" :loading="loading" @click="load(true)">重新扫描</el-button>
    </div>

    <el-alert v-if="error" type="error" :closable="false" show-icon style="margin-top: 4px"
      :title="`评估失败：${error}（节点需安装 iw：apt install iw，或 NetworkManager 可用）`" />

    <template v-if="report">
      <!-- 当前连接 -->
      <div class="card">
        <div class="card-head"><el-icon><Aim /></el-icon><span>当前连接</span></div>
        <template v-if="report.current">
          <div style="display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 12px">
            <div>
              <div class="text-secondary" style="font-size: 12px">网络</div>
              <div style="font-weight: 600">{{ report.current.ssid }}</div>
            </div>
            <div>
              <div class="text-secondary" style="font-size: 12px">信号强度</div>
              <div :style="{ color: rssiColor(report.current.rssi), fontWeight: 600 }">
                {{ report.current.rssi }} dBm
                <el-progress
                  style="margin-top: 4px" :percentage="report.current.signal"
                  :stroke-width="6" :show-text="false"
                  :color="rssiColor(report.current.rssi)"
                />
              </div>
            </div>
            <div>
              <div class="text-secondary" style="font-size: 12px">信道 / 频段</div>
              <div style="font-weight: 600">
                CH {{ report.current.channel || '-' }} · {{ bandLabel(report.current.band) }}
                <span class="mono" style="font-size: 12px; font-weight: 400">({{ report.current.freq }} MHz)</span>
              </div>
            </div>
            <div>
              <div class="text-secondary" style="font-size: 12px">链路速率</div>
              <div class="mono">{{ report.current.bitrate || '-' }}</div>
            </div>
            <div>
              <div class="text-secondary" style="font-size: 12px">BSSID</div>
              <div class="mono" style="font-size: 12px">{{ report.current.bssid || '-' }}</div>
            </div>
          </div>
        </template>
        <div class="empty" v-else>未连接到 WiFi</div>
      </div>

      <!-- 信道占用图 -->
      <div class="card">
        <div class="card-head">
          <el-icon><Switch /></el-icon><span>信道占用评估</span>
          <span class="text-secondary" style="font-weight: 400; font-size: 12px">
            柱高 = 该信道可见 AP 数；颜色绿/黄/红 = 拥挤度；红针 = 当前信道
          </span>
        </div>
        <div style="display: grid; grid-template-columns: repeat(auto-fit, minmax(380px, 1fr)); gap: 12px">
          <div v-for="b in ['2.4', '5', '6']" :key="b">
            <div class="text-secondary" style="font-size: 12.5px; margin-bottom: 2px">{{ bandLabel(b) }}</div>
            <div :id="`chart-${b.replace('.', '')}`" style="height: 170px" />
          </div>
        </div>
      </div>

      <!-- 评估建议 -->
      <div class="card" v-if="report.recommendations?.length">
        <div class="card-head"><el-icon><Aim /></el-icon><span>评估建议</span></div>
        <el-table :data="report.recommendations" size="small">
          <el-table-column label="类型" width="90">
            <template #default="{ row }">
              <el-tag size="small" :type="row.kind === 'ssid' ? 'success' : 'warning'">
                {{ row.kind === 'ssid' ? '切换网络' : '信道优化' }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="目标" min-width="140">
            <template #default="{ row }">
              <span v-if="row.kind === 'ssid'"><b>{{ row.ssid }}</b>（用户</span>
              <span v-else>信道 {{ row.channel }}（{{ bandLabel(row.band) }}）</span>
            </template>
          </el-table-column>
          <el-table-column label="信号" width="90">
            <template #default="{ row }">
              <span :style="{ color: row.rssi ? rssiColor(row.rssi) : 'inherit' }">
                {{ row.rssi != null ? `${row.rssi} dBm` : '-' }}
              </span>
            </template>
          </el-table-column>
          <el-table-column prop="reason" label="原因" min-width="260" />
          <el-table-column label="操作" width="90" v-if="report.guard?.enabled">
            <template #default="{ row }">
              <el-button
                v-if="row.kind === 'ssid' && row.ssid !== report.current?.ssid"
                size="small" type="primary" link :loading="switching === row.ssid"
                @click="switchTo(row.ssid)"
              >切换</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <!-- AP 列表 -->
      <div class="card">
        <div class="card-head"><el-icon><Connection /></el-icon><span>附近网络（{{ report.ap_list?.length ?? 0 }}）</span></div>
        <el-table :data="report.ap_list ?? []" size="small" max-height="300">
          <el-table-column prop="ssid" label="SSID" min-width="140">
            <template #default="{ row }">
              {{ row.ssid || '(隐藏)' }}
              <el-tag v-if="row.ssid === report.current?.ssid" size="small" type="success" style="margin-left: 6px">当前</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="信号" width="130">
            <template #default="{ row }">
              <span :style="{ color: rssiColor(row.rssi), fontWeight: 600 }">{{ row.rssi }} dBm</span>
              <el-progress
                style="margin-top: 3px; width: 80px" :percentage="row.signal"
                :stroke-width="4" :show-text="false" :color="rssiColor(row.rssi)"
              />
            </template>
          </el-table-column>
          <el-table-column prop="channel" label="信道" width="70">
            <template #default="{ row }">CH {{ row.channel || '-' }}</template>
          </el-table-column>
          <el-table-column label="频段" width="90">
            <template #default="{ row }">{{ bandLabel(row.band) }}</template>
          </el-table-column>
          <el-table-column prop="security" label="安全" width="90" />
          <el-table-column prop="bssid" label="BSSID" min-width="160">
            <template #default="{ row }"><span class="mono" style="font-size: 12px">{{ row.bssid }}</span></template>
          </el-table-column>
          <el-table-column label="操作" width="90">
            <template #default="{ row }">
              <el-button
                v-if="row.ssid && row.ssid !== report.current?.ssid"
                size="small" type="primary" link :loading="switching === row.ssid"
                :disabled="!row.ssid" @click="switchTo(row.ssid)"
              >切换</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <!-- 自动切换引擎 -->
      <div class="card">
        <div class="card-head">
          <el-icon><Switch /></el-icon>
          <span>自动切换引擎</span>
          <el-switch
            v-model="guardForm.enabled" size="small"
            active-text="开启" inactive-text="关闭"
            style="margin-left: 10px"
          />
          <span class="text-secondary" style="font-weight: 400; font-size: 12px; margin-left: 8px">
            当前信号低于阈值时，在白名单中选信号更优网络切换；切换后验证连通性，失败自动回退
          </span>
        </div>

        <el-form label-width="110px" size="small" style="margin-top: 6px">
          <el-form-item label="切换阈值">
            <div style="display: flex; align-items: center; gap: 10px; width: 360px">
              <el-slider
                v-model="guardForm.threshold" :min="-90" :max="-50" :step="1"
                :format-tooltip="(v: number) => `${v} dBm`"
              />
              <span class="mono" style="width: 70px">{{ guardForm.threshold }} dBm</span>
            </div>
            <div class="text-secondary" style="font-size: 12px">-75 为保守值；信号低于该值才触发评估</div>
          </el-form-item>
        </el-form>

        <div class="card-head" style="margin-top: 4px">
          <span style="font-size: 13px">备选网络白名单（只切这些，绝不连陌生热点）</span>
          <el-button size="small" @click="addRow">+ 添加</el-button>
        </div>
        <el-table :data="guardForm.rows" size="small">
          <el-table-column label="SSID" min-width="180">
            <template #default="{ row }">
              <el-input v-model="row.ssid" placeholder="WiFi 名称" size="small" />
            </template>
          </el-table-column>
          <el-table-column label="密码" min-width="200">
            <template #default="{ row }">
              <el-input
                v-model="row.password" type="password" show-password size="small"
                placeholder="留空 = 保持已保存的密码" autocomplete="new-password"
              />
            </template>
          </el-table-column>
          <el-table-column label="操作" width="80">
            <template #default="{ $index }">
              <el-button size="small" text type="danger" @click="removeRow($index)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
        <div style="margin-top: 12px; display: flex; gap: 10px; align-items: center">
          <el-button type="primary" :loading="saving" @click="saveConfig">保存配置</el-button>
          <span class="text-secondary" style="font-size: 12px">
            {{ report.guard?.enabled ? '引擎运行中' : '引擎已暂停' }}
            <template v-if="report.guard?.last_switch">
              · 最近切换 {{ report.guard.last_switch }}
            </template>
          </span>
        </div>

        <!-- 切换日志 -->
        <template v-if="report.guard?.log?.length">
          <div style="margin-top: 14px; font-size: 12.5px; color: var(--el-text-color-secondary)">最近动作</div>
          <el-table :data="[...(report.guard?.log ?? [])].reverse()" size="small" max-height="200" style="margin-top: 4px">
            <el-table-column label="时间" width="160">
              <template #default="{ row }"><span class="mono" style="font-size: 12px">{{ fmtTs(row.ts) }}</span></template>
            </el-table-column>
            <el-table-column label="事件" width="80">
              <template #default="{ row }">
                <el-tag size="small" :type="row.ok ? 'success' : 'danger'">
                  {{ ({ switch: '切换', check: '巡检', config: '配置' } as Record<string, string>)[String(row.event)] || row.event }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="目标" min-width="140">
              <template #default="{ row }">{{ (row.from ? row.from + ' → ' : '') + (row.to || '-') }}</template>
            </el-table-column>
            <el-table-column prop="msg" label="说明" min-width="240" />
          </el-table>
        </template>
      </div>
    </template>

    <div class="empty" v-else-if="!loading && !error" style="padding: 24px 0; text-align: center">
      点「重新扫描」评估当前 WiFi 信道质量
    </div>
  </div>
</template>

<style scoped>
.wifi-panel {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.card {
  background: var(--el-bg-color);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  padding: 14px;
}
.card-head {
  display: flex;
  align-items: center;
  gap: 6px;
  font-weight: 600;
  margin-bottom: 10px;
}
.text-secondary {
  color: var(--el-text-color-secondary);
}
.empty {
  color: var(--el-text-color-secondary);
  font-size: 13px;
}
</style>