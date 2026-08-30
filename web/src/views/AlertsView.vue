<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Plus, Delete, Edit, Upload, Refresh, Check } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'

import { api } from '@/api/client'

interface AlertRule {
  id: number
  node_id: string
  name: string
  rule_yaml: string
  enabled: boolean
  updated_at: string
}
interface AlertEvent {
  id: number
  node_id: string
  kind: string
  rule: string
  message: string
  read: boolean
  created_at: string
}

const rules = ref<AlertRule[]>([])
const events = ref<AlertEvent[]>([])
const nodes = ref<any[]>([])
const loading = ref(false)
const activeTab = ref('rules')

const dialogVisible = ref(false)
const editing = ref<AlertRule | null>(null)
const form = ref({ node_id: '', name: '', metric: 'cpu_percent', op: '>', threshold: 80, cooldown_sec: 300 })

async function load() {
  loading.value = true
  try {
    const [r, e, n] = await Promise.all([
      api.listAlertRules(),
      api.listAlertEvents(200),
      api.listNodes(),
    ])
    rules.value = r.rules
    events.value = e.events
    nodes.value = n
  } catch (e: any) {
    ElMessage.error(`加载失败：${e?.message || ''}`)
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editing.value = null
  form.value = { node_id: '', name: '', metric: 'cpu_percent', op: '>', threshold: 80, cooldown_sec: 300 }
  dialogVisible.value = true
}

function openEdit(rule: AlertRule) {
  editing.value = rule
  form.value = {
    node_id: rule.node_id,
    name: rule.name,
    metric: 'cpu_percent',
    op: '>',
    threshold: 80,
    cooldown_sec: 300,
  }
  // 尝试从 YAML 解析回填
  try {
    const parsed = JSON.parse(JSON.stringify(rule.rule_yaml ? rule.rule_yaml.split('\n').reduce((acc: any, line: string) => {
      const m = line.match(/^(\w+):\s*(.+)$/)
      if (m) acc[m[1]] = m[2]
      return acc
    }, {}) : {}))
    if (parsed.metric) form.value.metric = parsed.metric
    if (parsed.op) form.value.op = parsed.op
    if (parsed.threshold) form.value.threshold = Number(parsed.threshold)
    if (parsed.cooldown_sec) form.value.cooldown_sec = Number(parsed.cooldown_sec)
  } catch {
    /* 解析失败用默认 */
  }
  dialogVisible.value = true
}

async function saveRule() {
  if (!form.value.name || !form.value.metric) {
    ElMessage.warning('请填写规则名称和指标')
    return
  }
  try {
    const body = {
      node_id: form.value.node_id || undefined,
      name: form.value.name,
      metric: form.value.metric,
      op: form.value.op,
      threshold: Number(form.value.threshold),
      cooldown_sec: Number(form.value.cooldown_sec),
    }
    if (editing.value) {
      const yaml = [
        `name: ${body.name}`,
        `metric: ${body.metric}`,
        `op: ${body.op}`,
        `threshold: ${body.threshold}`,
        `cooldown_sec: ${body.cooldown_sec}`,
      ].join('\n')
      await api.updateAlertRule(editing.value.id, { name: body.name, rule_yaml: yaml + '\n' })
    } else {
      await api.createAlertRule(body)
    }
    dialogVisible.value = false
    ElMessage.success('保存成功')
    await load()
  } catch (e: any) {
    ElMessage.error(`保存失败：${e?.message || ''}`)
  }
}

async function deleteRule(rule: AlertRule) {
  const ok = await ElMessageBox.confirm(`删除规则「${rule.name}」？`, '删除确认', { type: 'warning' }).catch(() => false)
  if (!ok) return
  try {
    await api.deleteAlertRule(rule.id)
    ElMessage.success('已删除')
    await load()
  } catch (e: any) {
    ElMessage.error(`删除失败：${e?.message || ''}`)
  }
}

async function deployRule(rule: AlertRule) {
  try {
    const nodeId = rule.node_id || (await pickNode()) || ''
    if (!nodeId) return
    const r = await api.deployAlertRule(nodeId)
    ElMessage.success(`已下发 ${r.count} 条规则到节点`)
  } catch (e: any) {
    ElMessage.error(`下发失败：${e?.message || ''}`)
  }
}

async function pickNode(): Promise<string> {
  const online = nodes.value.filter((n) => n.status === 'online')
  if (online.length === 0) {
    ElMessage.warning('没有在线节点')
    return ''
  }
  try {
    const { value } = await ElMessageBox.prompt(
      '下发规则到哪个节点？（输入节点 ID）',
      '选择节点',
      { inputValue: online[0].id, inputPlaceholder: '节点 ID' },
    )
    return value
  } catch {
    return ''
  }
}

async function markRead() {
  try {
    await api.markAlertEventsRead()
    ElMessage.success('已全部标记已读')
    await load()
  } catch (e: any) {
    ElMessage.error(`操作失败：${e?.message || ''}`)
  }
}

onMounted(load)
</script>

<template>
  <div style="display: flex; flex-direction: column; gap: 12px">
    <div style="display: flex; align-items: center; gap: 8px">
      <span style="font-weight: 600">告警中心</span>
      <span style="flex: 1"></span>
      <el-button size="small" :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
    </div>

    <el-tabs v-model="activeTab">
      <!-- 规则管理 -->
      <el-tab-pane label="告警规则" name="rules">
        <div style="display: flex; justify-content: flex-end; margin-bottom: 8px">
          <el-button size="small" type="primary" :icon="Plus" @click="openCreate">新建规则</el-button>
        </div>
        <el-table :data="rules" size="small" v-loading="loading">
          <el-table-column prop="id" label="ID" width="60" />
          <el-table-column prop="name" label="规则名" min-width="120" />
          <el-table-column label="规则内容（YAML）" min-width="260">
            <template #default="{ row }">
              <pre style="margin: 0; font-size: 11.5px; white-space: pre-wrap">{{ row.rule_yaml }}</pre>
            </template>
          </el-table-column>
          <el-table-column prop="node_id" label="节点" width="150">
            <template #default="{ row }">{{ row.node_id || '全局' }}</template>
          </el-table-column>
          <el-table-column label="状态" width="80">
            <template #default="{ row }">
              <el-tag size="small" :type="row.enabled ? 'success' : 'info'">{{ row.enabled ? '启用' : '停用' }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="180">
            <template #default="{ row }">
              <el-button size="small" text :icon="Edit" @click="openEdit(row)">编辑</el-button>
              <el-button size="small" text :icon="Upload" @click="deployRule(row)">下发</el-button>
              <el-button size="small" text type="danger" :icon="Delete" @click="deleteRule(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>

      <!-- 告警事件 -->
      <el-tab-pane label="告警事件" name="events">
        <div style="display: flex; justify-content: flex-end; margin-bottom: 8px">
          <el-button size="small" :icon="Check" @click="markRead">全部标记已读</el-button>
        </div>
        <el-table :data="events" size="small" v-loading="loading">
          <el-table-column prop="id" label="ID" width="60" />
          <el-table-column label="时间" width="170">
            <template #default="{ row }">{{ new Date(row.created_at).toLocaleString() }}</template>
          </el-table-column>
          <el-table-column prop="node_id" label="节点" width="130" />
          <el-table-column prop="kind" label="类型" width="110">
            <template #default="{ row }">
              <el-tag size="small" :type="row.kind === 'alert_fired' ? 'danger' : 'warning'">{{ row.kind }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="message" label="消息" min-width="300" />
          <el-table-column label="已读" width="70">
            <template #default="{ row }">
              <el-tag size="small" :type="row.read ? 'info' : 'danger'">{{ row.read ? '已读' : '未读' }}</el-tag>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>
    </el-tabs>

    <!-- 规则编辑弹窗 -->
    <el-dialog v-model="dialogVisible" :title="editing ? '编辑规则' : '新建规则'" width="520px">
      <el-form label-width="90px">
        <el-form-item label="节点">
          <el-select v-model="form.node_id" clearable placeholder="留空=全局规则">
            <el-option v-for="n in nodes" :key="n.id" :label="n.hostname || n.id" :value="n.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="规则名" required>
          <el-input v-model="form.name" placeholder="如 cpu_high" />
        </el-form-item>
        <el-form-item label="指标">
          <el-select v-model="form.metric">
            <el-option label="CPU 使用率 (%)" value="cpu_percent" />
            <el-option label="内存使用率 (%)" value="mem_used_percent" />
            <el-option label="磁盘使用率 (%)" value="disk_used_percent" />
            <el-option label="负载 load1" value="load1" />
            <el-option label="温度 (°C)" value="temperature_celsius" />
          </el-select>
        </el-form-item>
        <el-form-item label="条件">
          <el-select v-model="form.op" style="width: 80px">
            <el-option label=">" value=">" />
            <el-option label="<" value="<" />
          </el-select>
          <el-input-number v-model="form.threshold" style="margin-left: 8px" :min="0" :precision="1" />
        </el-form-item>
        <el-form-item label="冷却(秒)">
          <el-input-number v-model="form.cooldown_sec" :min="0" :step="60" />
          <span class="text-secondary" style="font-size: 12px; margin-left: 8px">命中后冷却，避免反复刷屏</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="saveRule">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>
