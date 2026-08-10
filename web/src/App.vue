<script setup>
import { computed, onMounted, reactive, ref, watch } from 'vue'
import {
  Activity, Archive, ChevronRight, CircleUserRound, Database, Eye, FileClock,
  Copy, Download, Gauge, HardDriveUpload, KeyRound, LayoutDashboard, LogOut, Network, Pencil, Play, Plus,
  RefreshCw, Server, ShieldCheck, Square, TerminalSquare, Trash2, TriangleAlert, Users
} from '@lucide/vue'
import { api, csrfToken, formatBytes, formatTime } from './api'
import packageMetadata from '../package.json'

const appVersion = packageMetadata.version

const user = ref(null)
const authConfig = ref({ local_enabled: true, oidc_enabled: false })
const page = ref('dashboard')
const loading = ref(true)
const busy = ref(false)
const error = ref('')
const notice = ref('')
const dashboard = ref({})
const hosts = ref([])
const media = ref([])
const jobs = ref([])
const instances = ref([])
const clusters = ref([])
const backupPlans = ref([])
const backupRuns = ref([])
const monitorInstance = ref(null)
const monitorHistory = ref([])
const users = ref([])
const audits = ref([])
const secrets = ref([])
const selectedJob = ref(null)
const jobLogs = ref([])
const uploadProgress = ref(0)
const revealedSecret = ref(null)
const generatedScript = ref(null)
let eventSource

const loginForm = reactive({ username: '', password: '' })
const hostForm = reactive({ name: '', address: '', ssh_port: 22, ssh_user: 'aimops', private_key: '' })
const privateKeyFilename = ref('')
const editingHost = ref(null)
const editHostForm = reactive({ name: '', address: '', ssh_port: 22, ssh_user: 'aimops', private_key: '' })
const editPrivateKeyFilename = ref('')
const userForm = reactive({ username: '', password: '', role: 'viewer' })
const deploy = reactive({
  name: '', mode: 'standalone', version: '8.0.46', port: 3306, bind_address: '0.0.0.0', media_id: 0,
  mgr_port: 33061, mgr_allowlist: '', mgr_group_name: '', mgr_recovery_user: 'aim_mgr',
  deploy_router: false, router_rw_port: 6460, router_cluster_name: 'MGR01', mgr_admin_user: 'aim_cluster_admin', mgr_admin_password: '',
  replication_user: 'aim_repl', source_host: '', source_port: 3306, source_password: '', replica_host: '%',
  root_password: '', replication_password: '', mgr_recovery_password: '', nodes: [{ host_id: 0, local_ip: '', router_ip: '', server_id: 0 }]
})
const backupForm = reactive({
  name: '', instance_id: 0, schedule: '0 2 * * *', all_databases: true,
  databases: '', retention_days: 30, retention_count: 30, enabled: true
})
const pendingDestructive = ref(null)
const confirmation = ref('')
const pendingCleanup = ref(null)
const cleanupConfirmationInput = ref('')

const navigation = computed(() => [
  { id: 'dashboard', label: '运行总览', icon: LayoutDashboard },
  { id: 'hosts', label: '主机资源', icon: Server },
  { id: 'media', label: '安装介质', icon: Archive },
  { id: 'deploy', label: '部署向导', icon: HardDriveUpload, roles: ['admin', 'operator'] },
  { id: 'instances', label: 'MySQL 实例', icon: Database },
  { id: 'backups', label: '备份中心', icon: Archive },
  { id: 'monitor', label: '健康监控', icon: Activity },
  { id: 'clusters', label: '集群拓扑', icon: Network },
  { id: 'jobs', label: '任务中心', icon: TerminalSquare },
  { id: 'secrets', label: '密码保险箱', icon: KeyRound, roles: ['admin'] },
  { id: 'users', label: '用户权限', icon: Users, roles: ['admin'] },
  { id: 'audit', label: '审计日志', icon: FileClock, roles: ['admin'] }
].filter(item => !item.roles || item.roles.includes(user.value?.role)))

const currentTitle = computed(() => navigation.value.find(item => item.id === page.value)?.label || 'AIM 控制台')
const canOperate = computed(() => ['admin', 'operator'].includes(user.value?.role))
const isAdmin = computed(() => user.value?.role === 'admin')
const compatibleMedia = computed(() => {
  const selectedHosts = deploy.nodes
    .map(node => hosts.value.find(host => host.id === Number(node.host_id)))
    .filter(Boolean)
  return media.value.filter(item => {
    if (item.version !== deploy.version) return false
    return selectedHosts.every(host => {
      const facts = host.facts || {}
      const arch = facts.architecture === 'amd64' ? 'x86_64' : facts.architecture === 'arm64' ? 'aarch64' : facts.architecture
      return (!arch || arch === item.architecture) && (!facts.glibc || compareVersion(facts.glibc, item.glibc) >= 0)
    })
  }).sort((a, b) => Number(a.minimal) - Number(b.minimal) || compareVersion(b.glibc, a.glibc))
})

watch(() => deploy.mode, mode => {
  const count = mode === 'mgr' ? 3 : mode === 'replication' ? 2 : 1
  deploy.nodes = Array.from({ length: count }, (_, index) => deploy.nodes[index] || { host_id: 0, local_ip: '', router_ip: '', server_id: 0 })
  if (mode !== 'mgr') deploy.deploy_router = false
})

watch([() => deploy.version, () => deploy.nodes.map(node => node.host_id).join(','), compatibleMedia], () => {
  if (!compatibleMedia.value.some(item => item.id === Number(deploy.media_id))) {
    deploy.media_id = compatibleMedia.value[0]?.id || 0
  }
})

watch(deploy, () => { generatedScript.value = null }, { deep: true })

function compareVersion(left, right) {
  const a = String(left || '').split('.').map(Number)
  const b = String(right || '').split('.').map(Number)
  const length = Math.max(a.length, b.length)
  for (let index = 0; index < length; index += 1) {
    const delta = (a[index] || 0) - (b[index] || 0)
    if (delta) return delta
  }
  return 0
}

function flash(message) {
  notice.value = message
  window.setTimeout(() => { if (notice.value === message) notice.value = '' }, 3500)
}

async function run(action) {
  error.value = ''
  busy.value = true
  try { return await action() } catch (err) {
    if (err.status === 401) user.value = null
    error.value = err.message
    throw err
  } finally { busy.value = false }
}

async function restoreSession() {
	try { authConfig.value = await api('/auth/config') } catch (_) { /* keep local fallback */ }
  try {
    const session = await api('/session')
    user.value = session.user
    csrfToken.value = session.csrf_token
    await refreshAll()
  } catch (_) {
    user.value = null
  } finally { loading.value = false }
}

async function login() {
  await run(async () => {
    const session = await api('/session', { method: 'POST', body: JSON.stringify(loginForm) })
    user.value = session.user
    csrfToken.value = session.csrf_token
    loginForm.password = ''
    await refreshAll()
  }).catch(() => {})
}

function loginWithLazyCat() {
  window.location.assign('/api/v1/oidc/login')
}

async function logout() {
  await api('/session', { method: 'DELETE' }).catch(() => {})
  user.value = null
  csrfToken.value = ''
  if (eventSource) eventSource.close()
}

async function refreshAll() {
  const requests = [
    api('/dashboard'), api('/hosts'), api('/media'), api('/jobs'), api('/instances'), api('/clusters'), api('/backups/plans'), api('/backups/runs')
  ]
  const [dash, hostList, mediaList, jobList, instanceList, clusterList, planList, runList] = await Promise.all(requests)
  dashboard.value = dash
  hosts.value = hostList
  media.value = mediaList
  jobs.value = jobList
  instances.value = instanceList
  clusters.value = clusterList
  backupPlans.value = planList
  backupRuns.value = runList
  if (isAdmin.value) {
    const [userList, auditList, secretList] = await Promise.all([api('/users'), api('/audit'), api('/secrets')])
    users.value = userList
    audits.value = auditList
    secrets.value = secretList
  }
}

async function refreshPage() {
  await run(refreshAll).catch(() => {})
}

async function createHost() {
  await run(async () => {
    await api('/hosts', { method: 'POST', body: JSON.stringify(hostForm) })
    Object.assign(hostForm, { name: '', address: '', ssh_port: 22, ssh_user: 'aimops', private_key: '' })
    privateKeyFilename.value = ''
    hosts.value = await api('/hosts')
    flash('主机已保存，请确认 SSH 指纹后执行探测')
  }).catch(() => {})
}

async function importPrivateKey(event) {
	await loadPrivateKey(event, hostForm, privateKeyFilename)
}

async function importEditPrivateKey(event) {
	await loadPrivateKey(event, editHostForm, editPrivateKeyFilename)
}

async function loadPrivateKey(event, target, filename) {
  const file = event.target.files?.[0]
  if (!file) return
  const value = await file.text()
  if (!value.includes('-----BEGIN OPENSSH PRIVATE KEY-----') || !value.includes('-----END OPENSSH PRIVATE KEY-----')) {
    error.value = '请选择 aim-copy-id 生成的 OpenSSH 私钥文件，不要选择 .pub 公钥'
    event.target.value = ''
    return
  }
  target.private_key = `${value.trim()}\n`
  filename.value = file.name
  error.value = ''
  event.target.value = ''
}

function startEditHost(host) {
  editingHost.value = host
  Object.assign(editHostForm, {
    name: host.name,
    address: host.address,
    ssh_port: host.ssh_port,
    ssh_user: host.ssh_user,
    private_key: ''
  })
  editPrivateKeyFilename.value = ''
  error.value = ''
  window.requestAnimationFrame(() => document.getElementById('edit-host-panel')?.scrollIntoView({ behavior: 'smooth', block: 'start' }))
}

function cancelEditHost() {
  editingHost.value = null
  Object.assign(editHostForm, { name: '', address: '', ssh_port: 22, ssh_user: 'aimops', private_key: '' })
  editPrivateKeyFilename.value = ''
}

async function updateHost() {
  if (!editingHost.value) return
  await run(async () => {
    const result = await api(`/hosts/${editingHost.value.id}`, { method: 'PATCH', body: JSON.stringify(editHostForm) })
    hosts.value = await api('/hosts')
    cancelEditHost()
    flash(result.connection_reset ? '连接信息已更新，请重新确认 SSH 主机指纹' : '主机名称已更新')
  }).catch(() => {})
}

async function trustFingerprint(host) {
  await run(async () => {
    const scan = await api(`/hosts/${host.id}/fingerprint`, { method: 'POST', body: '{}' })
    if (!window.confirm(`请在目标主机核对 SSH 指纹：\n\n${scan.fingerprint}\n\n确认完全一致后继续。`)) return
    await api(`/hosts/${host.id}/fingerprint`, { method: 'POST', body: JSON.stringify({ confirm: scan.fingerprint }) })
    hosts.value = await api('/hosts')
    flash('SSH 主机指纹已固定')
  }).catch(() => {})
}

async function probeHost(host) {
  await run(async () => {
    try {
      await api(`/hosts/${host.id}/probe`, { method: 'POST', body: JSON.stringify({ ports: [3306, 8023, 33061, 18023] }) })
      flash(`${host.name} 探测完成`)
    } finally {
      hosts.value = await api('/hosts')
    }
  }).catch(() => {})
}

async function deleteHost(host) {
  if (!host.can_delete) {
    error.value = `无法删除主机“${host.name}”：${host.delete_block_reason || '该主机当前仍有关联资源'}`
    return
  }
  const confirmed = window.confirm(
    `确认从控制台删除主机“${host.name}”？\n\n` +
    '这只会删除控制台中的主机记录和加密 SSH 私钥，不会连接远端执行卸载或清理。\n' +
    '系统仅允许删除没有受管 MySQL 实例、且没有运行中或待核实任务的主机。历史任务日志仍会保留。'
  )
  if (!confirmed) return
  await run(async () => {
    await api(`/hosts/${host.id}`, { method: 'DELETE' })
    if (editingHost.value?.id === host.id) cancelEditHost()
    hosts.value = await api('/hosts')
    dashboard.value = await api('/dashboard')
    flash(`已删除主机 ${host.name}`)
  }).catch(() => {})
}

async function uploadMedia(event) {
  const file = event.target.files?.[0]
  if (!file) return
  await uploadSelectedFile(file)
  event.target.value = ''
}

async function uploadSelectedFile(file) {
  await run(async () => {
    uploadProgress.value = 0
    const resumeKey = `aim-upload:${file.name}:${file.size}`
    let upload
    const savedID = window.localStorage.getItem(resumeKey)
    if (savedID) {
      upload = await api(`/media/uploads/${savedID}`).catch(() => null)
      if (!upload || upload.filename !== file.name || upload.expected_size !== file.size) {
        window.localStorage.removeItem(resumeKey)
        upload = null
      }
    }
    if (upload?.status === 'complete') {
      window.localStorage.removeItem(resumeKey)
      media.value = await api('/media')
      flash('安装包已完成校验')
      return
    }
    if (!upload) {
      upload = await api('/media/uploads', { method: 'POST', body: JSON.stringify({ filename: file.name, size: file.size }) })
      window.localStorage.setItem(resumeKey, upload.id)
    }
    const chunkSize = upload.chunk_size
    const chunks = Math.ceil(file.size / chunkSize)
    const received = new Set(upload.received_chunks || [])
    let completed = received.size
    uploadProgress.value = Math.round((completed / chunks) * 100)
    for (let index = 0; index < chunks; index += 1) {
      if (received.has(index)) continue
      const chunk = file.slice(index * chunkSize, Math.min(file.size, (index + 1) * chunkSize))
      let lastError
      for (let attempt = 0; attempt < 3; attempt += 1) {
        try {
          await api(`/media/uploads/${upload.id}/chunks/${index}`, { method: 'PUT', headers: { 'Content-Type': 'application/octet-stream' }, body: chunk })
          lastError = null
          break
        } catch (err) {
          lastError = err
          if (attempt < 2) await new Promise(resolve => window.setTimeout(resolve, (attempt + 1) * 500))
        }
      }
      if (lastError) throw lastError
      completed += 1
      uploadProgress.value = Math.round((completed / chunks) * 100)
    }
    await api(`/media/uploads/${upload.id}/complete`, { method: 'POST', body: '{}' })
    window.localStorage.removeItem(resumeKey)
    media.value = await api('/media')
    flash('安装包上传并校验完成')
  }).catch(() => {})
}

function hostIPs(hostID) {
  return hosts.value.find(host => host.id === Number(hostID))?.facts?.ipv4 || []
}

function deploymentPayload() {
  const payload = JSON.parse(JSON.stringify(deploy))
  payload.media_id = Number(payload.media_id) || 0
  payload.port = Number(payload.port)
  payload.mgr_port = Number(payload.mgr_port)
  payload.router_rw_port = Number(payload.router_rw_port)
  payload.source_port = Number(payload.source_port)
  payload.nodes = payload.nodes.map(node => ({ host_id: Number(node.host_id), local_ip: node.local_ip, router_ip: node.router_ip, server_id: Number(node.server_id) || 0 }))
  return payload
}

async function createDeployment() {
  await run(async () => {
    const payload = deploymentPayload()
    const result = await api('/deployments', { method: 'POST', body: JSON.stringify(payload) })
    flash(`部署任务 ${result.job_id.slice(0, 8)} 已进入队列`)
    page.value = 'jobs'
    jobs.value = await api('/jobs')
    openJob(result.job_id)
  }).catch(() => {})
}

async function generateDeploymentScript() {
  await run(async () => {
    const payload = deploymentPayload()
    ;['root_password', 'replication_password', 'source_password', 'mgr_recovery_password', 'mgr_admin_password'].forEach(field => { payload[field] = '' })
    generatedScript.value = await api('/deployments/script', { method: 'POST', body: JSON.stringify(payload) })
    flash('已生成不含明文密码的目标机执行脚本')
    window.setTimeout(() => document.querySelector('.script-export-panel')?.scrollIntoView({ behavior: 'smooth', block: 'start' }), 0)
  }).catch(() => {})
}

async function copyDeploymentScript() {
  if (!generatedScript.value?.content) return
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(generatedScript.value.content)
  } else {
    const area = document.createElement('textarea')
    area.value = generatedScript.value.content
    area.style.position = 'fixed'
    area.style.opacity = '0'
    document.body.appendChild(area)
    area.select()
    document.execCommand('copy')
    area.remove()
  }
  flash('执行脚本已复制到剪贴板')
}

function downloadDeploymentScript() {
  if (!generatedScript.value?.content) return
  const blob = new Blob([generatedScript.value.content], { type: 'text/x-shellscript;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = generatedScript.value.filename
  document.body.appendChild(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(url)
  flash(`已下载 ${generatedScript.value.filename}`)
}

async function openJob(id) {
  if (eventSource) eventSource.close()
  selectedJob.value = await api(`/jobs/${id}`)
  jobLogs.value = []
  eventSource = new EventSource(`/api/v1/jobs/${id}/events`)
  eventSource.onmessage = event => jobLogs.value.push(JSON.parse(event.data))
  eventSource.addEventListener('complete', async () => {
    eventSource.close()
    selectedJob.value = await api(`/jobs/${id}`)
    jobs.value = await api('/jobs')
    instances.value = await api('/instances')
    clusters.value = await api('/clusters')
  })
  eventSource.onerror = () => eventSource.close()
}

async function retryJob(job) {
  await run(async () => {
    const result = await api(`/jobs/${job.id}/retry`, { method: 'POST', body: '{}' })
    jobs.value = await api('/jobs')
    flash(`已从未完成节点创建重试任务 ${result.job_id.slice(0, 8)}`)
    openJob(result.job_id)
  }).catch(() => {})
}

async function previewFailedCleanup(job) {
  await run(async () => {
    const result = await api(`/jobs/${job.id}/cleanup`, { method: 'POST', body: JSON.stringify({ dry_run: true }) })
    pendingCleanup.value = { source_job_id: job.id, preview_job_id: result.job_id, confirmation: result.confirmation }
    cleanupConfirmationInput.value = ''
    jobs.value = await api('/jobs')
    await openJob(result.job_id)
    flash('清理预览已创建；预览成功后输入确认文本执行')
  }).catch(() => {})
}

async function confirmFailedCleanup() {
  const pending = pendingCleanup.value
  if (!pending) return
  await run(async () => {
    const preview = await api(`/jobs/${pending.preview_job_id}`)
    if (preview.state !== 'complete') throw new Error('清理预览尚未成功完成，请先查看预览日志')
    const result = await api(`/jobs/${pending.source_job_id}/cleanup`, {
      method: 'POST',
      body: JSON.stringify({ preview_job_id: pending.preview_job_id, confirmation: cleanupConfirmationInput.value })
    })
    pendingCleanup.value = null
    cleanupConfirmationInput.value = ''
    jobs.value = await api('/jobs')
    await openJob(result.job_id)
  }).catch(() => {})
}

async function verifyJob(job) {
  await run(async () => {
    await api(`/jobs/${job.id}/verify`, { method: 'POST', body: '{}' })
    jobs.value = await api('/jobs')
    selectedJob.value = await api(`/jobs/${job.id}`)
    flash('远端状态已核实，现在可以从未完成节点重试')
  }).catch(() => {})
}

async function deleteJob(job) {
  const confirmed = window.confirm(
    `确认删除失败任务 ${job.id.slice(0, 12)}？\n\n` +
    '这只会删除控制台中的任务记录、节点关联和日志，不会回滚或卸载远端已经执行的操作。'
  )
  if (!confirmed) return
  await run(async () => {
    if (eventSource) eventSource.close()
    await api(`/jobs/${job.id}`, { method: 'DELETE' })
    jobs.value = await api('/jobs')
    dashboard.value = await api('/dashboard')
    selectedJob.value = null
    jobLogs.value = []
    flash('失败任务记录与日志已删除')
  }).catch(() => {})
}

async function instanceAction(instance, action) {
  await run(async () => {
    const result = await api(`/instances/${instance.id}/actions`, { method: 'POST', body: JSON.stringify({ action }) })
    page.value = 'jobs'
    jobs.value = await api('/jobs')
    openJob(result.job_id)
  }).catch(() => {})
}

async function createBackupPlan() {
  await run(async () => {
    const payload = {
      ...backupForm,
      instance_id: Number(backupForm.instance_id),
      databases: backupForm.databases.split(/[\n,]+/).map(value => value.trim()).filter(Boolean),
      retention_days: Number(backupForm.retention_days),
      retention_count: Number(backupForm.retention_count)
    }
    await api('/backups/plans', { method: 'POST', body: JSON.stringify(payload) })
    Object.assign(backupForm, { name: '', instance_id: 0, schedule: '0 2 * * *', all_databases: true, databases: '', retention_days: 30, retention_count: 30, enabled: true })
    backupPlans.value = await api('/backups/plans')
    flash('备份计划已保存，调度器会按 Cron 表达式执行')
  }).catch(() => {})
}

async function runBackupPlan(plan) {
  await run(async () => {
    await api(`/backups/plans/${plan.id}/run`, { method: 'POST', body: '{}' })
    backupRuns.value = await api('/backups/runs')
    flash(`备份任务已启动：${plan.name}`)
  }).catch(() => {})
}

async function deleteBackupPlan(plan) {
  if (!window.confirm(`确定删除备份计划“${plan.name}”吗？历史备份文件不会被立即删除。`)) return
  await run(async () => {
    await api(`/backups/plans/${plan.id}`, { method: 'DELETE' })
    backupPlans.value = await api('/backups/plans')
    flash('备份计划已删除')
  }).catch(() => {})
}

async function cancelBackupRun(runItem) {
  await run(async () => {
    await api(`/backups/runs/${runItem.id}/cancel`, { method: 'POST', body: '{}' })
    backupRuns.value = await api('/backups/runs')
    flash('已请求取消备份任务')
  }).catch(() => {})
}

function downloadBackup(runItem) {
  window.open(`/api/v1/backups/runs/${runItem.id}/download`, '_blank', 'noopener')
}

async function loadMetrics(instance) {
  await run(async () => {
    monitorInstance.value = instance
    const [current, history] = await Promise.all([
      api(`/monitor/instances/${instance.id}`),
      api(`/monitor/instances/${instance.id}/history`)
    ])
    monitorInstance.value = { ...instance, metrics: current }
    monitorHistory.value = history
  }).catch(() => {})
}

async function previewDestructive(instance, action) {
  await run(async () => {
    const result = await api(`/instances/${instance.id}/actions`, { method: 'POST', body: JSON.stringify({ action, dry_run: true }) })
    pendingDestructive.value = { instance, action, preview_job_id: result.job_id }
    confirmation.value = ''
    flash('预览任务已创建，确认前请等待预览成功')
  }).catch(() => {})
}

async function confirmDestructive() {
  const pending = pendingDestructive.value
  await run(async () => {
    const preview = await api(`/jobs/${pending.preview_job_id}`)
    if (preview.state !== 'complete') throw new Error('预览尚未成功完成')
    const result = await api(`/instances/${pending.instance.id}/actions`, {
      method: 'POST', body: JSON.stringify({ action: pending.action, preview_job_id: pending.preview_job_id, confirmation: confirmation.value })
    })
    pendingDestructive.value = null
    page.value = 'jobs'
    jobs.value = await api('/jobs')
    openJob(result.job_id)
  }).catch(() => {})
}

async function createUser() {
  await run(async () => {
    await api('/users', { method: 'POST', body: JSON.stringify(userForm) })
    Object.assign(userForm, { username: '', password: '', role: 'viewer' })
    users.value = await api('/users')
    flash('用户已创建')
  }).catch(() => {})
}

async function toggleUser(item) {
  await run(async () => {
    await api(`/users/${item.id}`, { method: 'PATCH', body: JSON.stringify({ role: item.role, active: !item.active }) })
    users.value = await api('/users')
    flash(item.active ? '用户已停用' : '用户已启用')
  }).catch(() => {})
}

async function revealSecret(secret) {
  await run(async () => {
    revealedSecret.value = await api(`/secrets/${secret.id}/reveal`, { method: 'POST', body: '{}' })
    audits.value = await api('/audit')
  }).catch(() => {})
}

function statusClass(state) {
  if (['online', 'running', 'complete', 'cleaned'].includes(state)) return 'success'
  if (['failed', 'error', 'missing'].includes(state)) return 'danger'
  if (['queued', 'preflight', 'transferring', 'running', 'pending', 'cleanup_running'].includes(state)) return 'warning'
  return 'neutral'
}

function metricAt(sample, key) {
  try { return JSON.parse(sample.metrics_json || '{}')[key] ?? '—' } catch (_) { return '—' }
}

onMounted(restoreSession)
</script>

<template>
  <div v-if="loading" class="loading-screen"><div class="brand-mark">aim<span>.sh</span></div><p>正在连接控制台…</p></div>

  <main v-else-if="!user" class="login-page">
    <section class="login-story">
      <div class="brand-mark light">aim<span>.sh</span></div>
      <p class="eyebrow">MYSQL LIFECYCLE CONTROL PLANE</p>
      <h1>让 MySQL 运维形成<br><em>可验证的管理闭环</em></h1>
      <p class="story-copy">支持 MySQL 5.6 / 5.7 / 8.0 / 8.4；从系统探测、安装包校验和单机、主从、三节点 MGR + MySQL Router 高可用入口部署，到在线备份、懒猫网盘归档与健康监控，在一处完成编排、审计和生命周期管理。</p>
      <div class="story-grid">
        <div><ShieldCheck /><strong>安全部署</strong><span>固定 SSH 指纹、最小 sudo 权限与部署前校验</span></div>
        <div><Archive /><strong>在线备份</strong><span>一致性转储、SHA-256 校验与懒猫网盘归档</span></div>
        <div><Activity /><strong>健康监控</strong><span>CPU、内存、磁盘、连接数与复制状态</span></div>
      </div>
    </section>
    <section class="login-panel">
      <form class="login-card" @submit.prevent="login">
        <p class="eyebrow">AIM CONSOLE</p><h2>登录内网控制台</h2><p>{{ authConfig.oidc_enabled ? '使用懒猫微服账号安全登录。' : '使用管理员分配的本地账号继续。' }}</p>
        <button v-if="authConfig.oidc_enabled" type="button" class="button primary wide" @click="loginWithLazyCat"><ShieldCheck /><span>使用懒猫账号登录</span><ChevronRight /></button>
        <template v-if="authConfig.local_enabled">
          <label>用户名<input v-model.trim="loginForm.username" autocomplete="username" required autofocus></label>
          <label>密码<input v-model="loginForm.password" type="password" autocomplete="current-password" required></label>
        </template>
        <div v-if="error" class="alert danger"><TriangleAlert />{{ error }}</div>
        <button v-if="authConfig.local_enabled" class="button secondary wide" :disabled="busy"><span>{{ busy ? '正在验证…' : '本地账号登录' }}</span><ChevronRight /></button>
      </form>
    </section>
  </main>

  <div v-else class="app-shell">
    <aside class="sidebar">
      <div class="sidebar-brand"><div class="brand-mark light compact console-brand-title">aim<span>.sh</span><strong>MySQL 控制台</strong></div><small class="app-version">版本 v{{ appVersion }}</small></div>
      <nav aria-label="主导航">
        <button v-for="item in navigation" :key="item.id" :class="{ active: page === item.id }" @click="page = item.id">
          <component :is="item.icon" /><span>{{ item.label }}</span>
        </button>
      </nav>
      <div class="sidebar-user"><CircleUserRound /><div><strong>{{ user.username }}</strong><span>{{ user.role }}</span></div><button title="退出登录" @click="logout"><LogOut /></button></div>
    </aside>

    <section class="workspace">
      <header class="topbar"><div><p class="eyebrow">AIM / {{ page.toUpperCase() }}</p><h1>{{ currentTitle }}</h1></div><button class="button ghost" :disabled="busy" @click="refreshPage"><RefreshCw :class="{ spinning: busy }" />刷新数据</button></header>
      <div v-if="error" class="alert danger global"><TriangleAlert />{{ error }}<button @click="error = ''">×</button></div>
      <div v-if="notice" class="alert success global"><ShieldCheck />{{ notice }}</div>

      <div v-if="page === 'dashboard'" class="page-stack">
        <section class="hero-card"><div><p class="eyebrow">CONTROL PLANE STATUS</p><h2>数据库基础设施，清晰可控。</h2><p>所有主机、介质、任务和实例状态均来自控制台当前记录。</p></div><Gauge /></section>
        <section class="metric-grid">
          <article><span class="metric-icon blue"><Server /></span><div><b>{{ dashboard.hosts || 0 }}</b><span>已纳管主机</span></div></article>
          <article><span class="metric-icon teal"><Database /></span><div><b>{{ dashboard.instances || 0 }}</b><span>MySQL 实例</span></div></article>
          <article><span class="metric-icon violet"><Network /></span><div><b>{{ dashboard.clusters || 0 }}</b><span>复制与 MGR 集群</span></div></article>
          <article><span class="metric-icon amber"><Activity /></span><div><b>{{ dashboard.running_jobs || 0 }}</b><span>正在执行任务</span></div></article>
        </section>
        <section class="panel"><div class="panel-head"><div><p class="eyebrow">RECENT ACTIVITY</p><h3>最近任务</h3></div><button class="link-button" @click="page = 'jobs'">查看全部 <ChevronRight /></button></div>
          <div class="table-wrap"><table><thead><tr><th>任务</th><th>类型</th><th>状态</th><th>创建时间</th></tr></thead><tbody><tr v-for="job in jobs.slice(0, 6)" :key="job.id" @click="page='jobs'; openJob(job.id)"><td class="mono">{{ job.id.slice(0, 8) }}</td><td>{{ job.kind }}</td><td><span class="status" :class="statusClass(job.state)">{{ job.state }}</span></td><td>{{ formatTime(job.created_at) }}</td></tr><tr v-if="!jobs.length"><td colspan="4" class="empty">暂无任务</td></tr></tbody></table></div>
        </section>
      </div>

      <div v-else-if="page === 'hosts'" class="page-stack">
        <section v-if="isAdmin" class="panel"><div class="panel-head"><div><p class="eyebrow">HOST ONBOARDING</p><h3>添加受管主机</h3></div><a class="button secondary" href="/downloads/aim-host-kit-2.4.14.tar.gz" download><Archive />下载 Host Kit 2.4.14</a></div>
          <div class="host-kit-guide">
            <div class="host-kit-intro"><span class="guide-icon"><TerminalSquare /></span><div><p class="eyebrow">HOST KIT 2.4.14</p><h3>先在管理电脑初始化目标主机</h3><p>支持 macOS、Linux 或 WSL；目标机支持 RHEL 系（含 OpenCloudOS）、Debian/Ubuntu 与 SUSE。工具会生成 AIM 专用密钥，识别目标机架构，复制 MySQL/Router 受限执行器，并提供失败部署的安全续跑与受限清理能力。已安装旧版的主机也应重新执行一次，幂等升级不会删除 MySQL 数据。</p></div><span class="safety-badge"><ShieldCheck />不会复制私钥到目标机</span></div>
            <div class="host-kit-steps">
              <article><span class="step-number">01</span><div><h4>下载并解压</h4><p>先点击下面的按钮下载安装包。下载完成后，在能够 SSH 登录目标服务器的管理电脑上打开终端并执行：</p><a class="button primary guide-download" href="/downloads/aim-host-kit-2.4.14.tar.gz" download><Archive />下载 Host Kit 2.4.14</a><p class="download-note">文件通常保存在系统的“下载”目录，文件名为 <code>aim-host-kit-2.4.14.tar.gz</code>。</p><pre><code>tar -xzf aim-host-kit-2.4.14.tar.gz
cd aim-host-kit-2.4.14</code></pre></div></article>
              <article><span class="step-number">02</span><div><h4>一键初始化目标机</h4><p>把示例地址替换为实际目标机。首次账号可以是 root，或具备 sudo 权限的运维账号：</p><pre><code>./aim-copy-id --install root@192.168.1.100</code></pre><p class="command-note">非 22 端口：<code>./aim-copy-id --install --port 2222 root@192.168.1.100</code></p></div></article>
              <article><span class="step-number">03</span><div><h4>导入专用私钥</h4><p>命令结束会打印私钥路径，默认是：</p><pre><code>~/.ssh/aim/aim_console_ed25519</code></pre><p class="command-note danger-text">不要选择 <code>aim_console_ed25519.pub</code>；带 <code>.pub</code> 的是公钥。</p></div></article>
              <article><span class="step-number">04</span><div><h4>保存并确认指纹</h4><p>下面填写目标机地址和端口，SSH 用户固定填写 <code>aimops</code>，导入上一步的私钥。保存后先核对 SSH SHA-256 指纹，再点击“重新探测”。</p></div></article>
            </div>
            <div class="host-kit-help"><ShieldCheck /><div><strong>没有 sudo 登录权限怎么办？</strong><p>先执行 <code>./aim-copy-id user@host</code>，文件复制完成后，工具会打印一条 <code>sudo .../install-staged-target.sh</code> 命令；请交给目标机管理员执行一次。每台目标机都要分别初始化。重复执行 Host Kit 是幂等操作，不会删除 MySQL 数据。</p></div></div>
          </div>
          <form class="form-grid host-form" @submit.prevent="createHost"><label>主机名称<input v-model.trim="hostForm.name" placeholder="node00" required></label><label>IP 或域名<input v-model.trim="hostForm.address" placeholder="172.20.23.90" required></label><label>SSH 端口<input v-model.number="hostForm.ssh_port" type="number" min="1" max="65535" required></label><label>SSH 用户<input v-model.trim="hostForm.ssh_user" required></label><label class="button secondary span-2">导入 aim-copy-id 生成的私钥<input type="file" hidden @change="importPrivateKey"></label><label class="span-2">专用 SSH 私钥 <small v-if="privateKeyFilename">已导入：{{ privateKeyFilename }}</small><textarea v-model="hostForm.private_key" rows="4" placeholder="也可以手动粘贴 -----BEGIN OPENSSH PRIVATE KEY-----" required></textarea></label><div class="form-actions span-2"><button class="button primary" :disabled="busy"><Plus />保存主机</button></div></form>
        </section>
        <section v-if="isAdmin && editingHost" id="edit-host-panel" class="panel"><div class="panel-head"><div><p class="eyebrow">EDIT HOST</p><h3>修改主机：{{ editingHost.name }}</h3></div><button class="button ghost compact-button" @click="cancelEditHost">取消</button></div>
          <form class="form-grid host-form" @submit.prevent="updateHost"><label>主机名称<input v-model.trim="editHostForm.name" required></label><label>IP 或域名<input v-model.trim="editHostForm.address" required></label><label>SSH 端口<input v-model.number="editHostForm.ssh_port" type="number" min="1" max="65535" required></label><label>SSH 用户<input v-model.trim="editHostForm.ssh_user" required></label><label class="button secondary span-2">重新导入专用私钥（可选）<input type="file" hidden @change="importEditPrivateKey"></label><label class="span-2">新 SSH 私钥 <small v-if="editPrivateKeyFilename">已导入：{{ editPrivateKeyFilename }}</small><textarea v-model="editHostForm.private_key" rows="4" placeholder="留空则继续使用原私钥；不要填写 .pub 公钥"></textarea><small>修改地址、端口、SSH 用户或私钥后，旧主机指纹会失效，必须重新确认。</small></label><div class="form-actions span-2"><button type="button" class="button ghost" @click="cancelEditHost">取消</button><button class="button primary" :disabled="busy"><Pencil />保存修改</button></div></form>
        </section>
        <section class="panel"><div class="panel-head"><div><p class="eyebrow">INVENTORY</p><h3>主机资源</h3></div></div>
          <div class="card-grid"><article v-for="host in hosts" :key="host.id" class="host-card"><div class="host-card-head"><span class="server-glyph"><Server /></span><div><h4>{{ host.name }}</h4><span class="mono">{{ host.address }}:{{ host.ssh_port }}</span></div><span class="status" :class="statusClass(host.status)">{{ host.status }}</span></div><dl><div><dt>系统</dt><dd>{{ host.facts?.os_name || '等待探测' }}</dd></div><div><dt>架构 / glibc</dt><dd>{{ host.facts?.architecture || '—' }} / {{ host.facts?.glibc || '—' }}</dd></div><div><dt>IPv4</dt><dd>{{ host.facts?.ipv4?.join(', ') || '—' }}</dd></div><div><dt>资源</dt><dd>{{ host.facts?.cpus || '—' }} CPU · {{ host.facts?.memory_mb || '—' }} MiB</dd></div></dl><div class="card-actions"><button v-if="isAdmin" class="button secondary" @click="startEditHost(host)"><Pencil />修改</button><button v-if="isAdmin && !host.host_key_fingerprint" class="button secondary" @click="trustFingerprint(host)"><KeyRound />确认指纹</button><button v-if="canOperate && host.host_key_fingerprint" class="button secondary" @click="probeHost(host)"><Activity />重新探测</button><span v-if="isAdmin" class="delete-host-action"><button class="button danger" :class="{ 'is-disabled': !host.can_delete }" :aria-disabled="!host.can_delete" :title="host.can_delete ? '删除控制台中的空主机记录' : host.delete_block_reason" @click="deleteHost(host)"><Trash2 />删除空主机</button><span v-if="!host.can_delete" class="action-tooltip" role="tooltip">{{ host.delete_block_reason || '该主机当前仍有关联资源' }}</span></span></div><p v-if="host.last_error" class="inline-error">{{ host.last_error }}</p></article><div v-if="!hosts.length" class="empty-card">还没有受管主机，请先添加专用 aimops SSH 账号。</div></div>
        </section>
      </div>

      <div v-else-if="page === 'media'" class="page-stack">
        <section v-if="canOperate" class="upload-zone"><Archive /><div><h3>上传 MySQL Generic 安装包</h3><p>支持从本机或懒猫网盘选择 .tar.xz、.tar.gz、.tgz 和 .tar，最大 2 GiB；自动分块续传并计算 SHA-256。</p></div><label class="button primary"><HardDriveUpload />选择软件包<input type="file" accept=".xz,.gz,.tgz,.tar" hidden @change="uploadMedia"></label><progress v-if="uploadProgress" :value="uploadProgress" max="100">{{ uploadProgress }}%</progress></section>
        <section class="panel"><div class="panel-head"><div><p class="eyebrow">PACKAGE LIBRARY</p><h3>安装介质库</h3></div></div><div class="table-wrap"><table><thead><tr><th>文件名</th><th>版本</th><th>glibc / 架构</th><th>大小</th><th>SHA-256</th></tr></thead><tbody><tr v-for="item in media" :key="item.id"><td><strong>{{ item.filename }}</strong><small v-if="item.minimal">minimal</small></td><td>{{ item.version }}</td><td>{{ item.glibc }} / {{ item.architecture }}</td><td>{{ formatBytes(item.size) }}</td><td class="mono digest">{{ item.sha256 }}</td></tr><tr v-if="!media.length"><td colspan="5" class="empty">暂无安装介质，也可以在部署时选择由目标机官方下载。</td></tr></tbody></table></div></section>
      </div>

      <div v-else-if="page === 'deploy'" class="page-stack">
        <section class="panel wizard"><div class="panel-head"><div><p class="eyebrow">DEPLOYMENT WIZARD</p><h3>创建 MySQL 部署任务</h3></div><span class="safety-badge"><ShieldCheck />先整体预检，再顺序执行</span></div>
          <form @submit.prevent="createDeployment">
            <fieldset><legend><span>01</span>基础规格</legend><div class="form-grid"><label>部署名称<input v-model.trim="deploy.name" placeholder="production-mysql" required></label><label>部署模式<select v-model="deploy.mode"><option value="standalone">单机实例</option><option value="source">仅主库</option><option value="replica">仅从库</option><option value="replication">一主一从</option><option value="mgr">三节点 MGR</option></select></label><label>MySQL 版本<input v-model.trim="deploy.version" placeholder="8.0.46" required></label><label>SQL 端口<input v-model.number="deploy.port" type="number" min="1" max="65535" required></label><label>绑定地址<input v-model.trim="deploy.bind_address" required></label><label>安装介质<select v-model.number="deploy.media_id"><option :value="0">目标机官方下载</option><option v-for="item in compatibleMedia" :key="item.id" :value="item.id">{{ item.filename }}</option></select><small v-if="compatibleMedia.length">已按所选主机的 glibc 与架构自动匹配</small><small v-else>暂无兼容介质，将由目标机官方下载</small></label></div></fieldset>
            <fieldset><legend><span>02</span>节点与网络</legend><div class="node-grid"><article v-for="(node, index) in deploy.nodes" :key="index"><div class="node-number">{{ String(index + 1).padStart(2, '0') }}</div><label>目标主机<select v-model.number="node.host_id" required><option :value="0" disabled>请选择</option><option v-for="host in hosts.filter(h => h.status === 'online')" :key="host.id" :value="host.id">{{ host.name }} · {{ host.address }}</option></select></label><label v-if="deploy.mode === 'mgr' || deploy.mode === 'replication'">业务网 IP<select v-model="node.local_ip" required><option value="" disabled>请选择探测到的 IP</option><option v-for="ip in hostIPs(node.host_id)" :key="ip">{{ ip }}</option></select></label><label v-if="deploy.mode === 'mgr' && deploy.deploy_router">Router 绑定 IP<select v-model="node.router_ip"><option value="">默认同业务网 IP</option><option v-for="ip in hostIPs(node.host_id)" :key="ip">{{ ip }}</option></select><small>必须是本机真实拥有的 IP，不能填写公网 NAT 映射地址</small></label><label v-if="deploy.mode !== 'standalone'">server_id<input v-model.number="node.server_id" type="number" min="1" max="4294967294" required></label><span v-if="deploy.mode === 'mgr'" class="node-role">{{ index === 0 ? 'BOOTSTRAP' : 'JOIN' }}</span></article></div></fieldset>
            <fieldset v-if="deploy.mode === 'mgr'"><legend><span>03</span>MGR 参数</legend><div class="form-grid"><label>MGR 通信端口<input v-model.number="deploy.mgr_port" type="number" min="1" max="65535" required></label><label>IP allowlist（可自动生成）<input v-model.trim="deploy.mgr_allowlist" placeholder="留空使用三个节点的精确 IP"></label><label>组 UUID（可自动生成）<input v-model.trim="deploy.mgr_group_name" placeholder="留空自动生成"></label><label>恢复账号<input v-model.trim="deploy.mgr_recovery_user"></label><label class="span-2">共享恢复密码（可自动生成）<input v-model="deploy.mgr_recovery_password" type="password" autocomplete="new-password" placeholder="留空由控制台生成"></label></div></fieldset>
            <fieldset v-if="deploy.mode === 'mgr'"><legend><span>04</span>MySQL Router（可选）</legend><div class="form-grid"><label class="check-label span-2"><input v-model="deploy.deploy_router" type="checkbox">MGR 完成后自动接管为 InnoDB Cluster，并在三个节点部署 MySQL Router</label><template v-if="deploy.deploy_router"><label>InnoDB Cluster 名称<input v-model.trim="deploy.router_cluster_name" placeholder="MGR01" required><small>这是 Router 使用的集群名称，不是上面的组 UUID</small></label><label>Router Classic 读写端口<input v-model.number="deploy.router_rw_port" type="number" min="1" max="65532" required><small>业务连接使用此端口；将同时占用 {{ deploy.router_rw_port + 1 }}、{{ deploy.router_rw_port + 2 }}、{{ deploy.router_rw_port + 3 }}</small></label><label>集群管理账号<input v-model.trim="deploy.mgr_admin_user" required><small>仅允许从三个业务网 IP 登录，不开放远程 root</small></label><label>集群管理密码（可自动生成）<input v-model="deploy.mgr_admin_password" type="password" autocomplete="new-password" placeholder="留空由控制台生成并加密保存"></label><div class="span-2 host-kit-help"><ShieldCheck /><div><strong>端口含义</strong><p><code>{{ deploy.router_rw_port }}</code> = Classic 读写，<code>{{ deploy.router_rw_port + 1 }}</code> = Classic 只读，<code>{{ deploy.router_rw_port + 2 }}</code> = X 协议读写，<code>{{ deploy.router_rw_port + 3 }}</code> = X 协议只读。应用通常连接三个节点任一 Router 的 <code>业务网IP:{{ deploy.router_rw_port }}</code>。</p></div></div></template></div></fieldset>
            <fieldset v-if="['source','replica','replication'].includes(deploy.mode)"><legend><span>03</span>复制参数</legend><div class="form-grid"><label>复制账号<input v-model.trim="deploy.replication_user"></label><label v-if="deploy.mode === 'source'">允许的从库地址<input v-model.trim="deploy.replica_host"></label><label v-if="deploy.mode === 'replica'">源库 IP<input v-model.trim="deploy.source_host" required></label><label v-if="deploy.mode === 'replica'">源库端口<input v-model.number="deploy.source_port" type="number" required></label><label v-if="deploy.mode === 'replica'" class="span-2">源库复制密码<input v-model="deploy.source_password" type="password" required></label><label v-else class="span-2">复制密码（可自动生成）<input v-model="deploy.replication_password" type="password" placeholder="留空由控制台生成"></label></div></fieldset>
            <fieldset><legend><span>{{ deploy.mode === 'mgr' ? '05' : ['source','replica','replication'].includes(deploy.mode) ? '04' : '03' }}</span>凭据</legend><div class="form-grid"><label class="span-2">MySQL root 密码（可自动生成）<input v-model="deploy.root_password" type="password" autocomplete="new-password" placeholder="留空由控制台生成并加密保存"></label></div></fieldset>
            <div class="wizard-footer"><div><ShieldCheck /><p><strong>提交后不会立即盲目安装</strong><span>控制台会先验证主机、端口、glibc、架构和安装包。</span></p></div><div class="wizard-actions"><button type="button" class="button secondary large" :disabled="busy || !hosts.length" @click="generateDeploymentScript"><TerminalSquare />生成执行脚本</button><button class="button primary large" :disabled="busy || !hosts.length"><Play />创建部署任务</button></div></div>
          </form>
        </section>
        <section v-if="generatedScript" class="panel script-export-panel"><div class="panel-head"><div><p class="eyebrow">PORTABLE AIM.SH WRAPPER</p><h3>可复制的目标机执行脚本</h3></div><span class="safety-badge"><ShieldCheck />不含明文密码</span></div><div class="script-export-body"><div class="script-scope"><strong>能力边界</strong><p>脚本覆盖单机、主库、从库、一主一从、三节点 MGR 和可选 MySQL Router。备份计划、懒猫网盘归档、监控历史与审计仍由 Web 控制台和受限执行器管理，不导出为一次性 aim.sh 命令。</p></div><ol><li v-for="instruction in generatedScript.instructions" :key="instruction">{{ instruction }}</li></ol><textarea :value="generatedScript.content" readonly rows="22" spellcheck="false"></textarea><div class="script-export-actions"><span><code>{{ generatedScript.filename }}</code> · 配置变化后请重新生成</span><button type="button" class="button secondary" @click="copyDeploymentScript"><Copy />复制脚本</button><button type="button" class="button primary" @click="downloadDeploymentScript"><Download />下载 .sh</button></div></div></section>
      </div>

      <div v-else-if="page === 'instances'" class="page-stack"><section class="panel"><div class="panel-head"><div><p class="eyebrow">INSTANCE LIFECYCLE</p><h3>MySQL 实例</h3></div></div><div class="table-wrap"><table><thead><tr><th>主机</th><th>实例</th><th>角色</th><th>状态</th><th>操作</th></tr></thead><tbody><tr v-for="instance in instances" :key="instance.id"><td><strong>{{ instance.host_name }}</strong><small>{{ instance.address }}</small></td><td><span class="mono">{{ instance.version }} :{{ instance.port }}</span></td><td>{{ instance.role }}</td><td><span class="status" :class="statusClass(instance.state)">{{ instance.state }}</span></td><td><div v-if="canOperate" class="row-actions"><button title="启动" @click="instanceAction(instance,'start')"><Play /></button><button title="停止" @click="instanceAction(instance,'stop')"><Square /></button><button title="状态检查" @click="instanceAction(instance,'status')"><Activity /></button><button v-if="isAdmin" class="warning" title="重新初始化" @click="previewDestructive(instance,'reinitialize')"><RefreshCw /></button><button v-if="isAdmin" class="danger" title="卸载" @click="previewDestructive(instance,'uninstall')"><Trash2 /></button></div></td></tr><tr v-if="!instances.length"><td colspan="5" class="empty">暂无由控制台管理的实例</td></tr></tbody></table></div></section>
        <section v-if="pendingDestructive" class="danger-panel"><TriangleAlert /><div><h3>{{ pendingDestructive.action === 'uninstall' ? '永久卸载实例' : '重新初始化实例' }}</h3><p>预览任务：<button class="mono-link" @click="page='jobs'; openJob(pendingDestructive.preview_job_id)">{{ pendingDestructive.preview_job_id }}</button></p><p>预览成功后，输入 <strong>{{ pendingDestructive.instance.address }}:{{ pendingDestructive.instance.port }}</strong> 才能执行。</p><input v-model.trim="confirmation" :placeholder="`${pendingDestructive.instance.address}:${pendingDestructive.instance.port}`"><div class="card-actions"><button class="button danger" @click="confirmDestructive">确认执行</button><button class="button ghost" @click="pendingDestructive=null">取消</button></div></div></section>
      </div>

      <div v-else-if="page === 'backups'" class="page-stack">
        <section v-if="canOperate" class="panel"><div class="panel-head"><div><p class="eyebrow">ONLINE BACKUP</p><h3>创建备份计划</h3></div><span class="safety-badge"><ShieldCheck />远端流式压缩 · SHA-256 校验</span></div>
          <form class="form-grid host-form" @submit.prevent="createBackupPlan"><label>计划名称<input v-model.trim="backupForm.name" placeholder="每天全库备份" required></label><label>MySQL 实例<select v-model.number="backupForm.instance_id" required><option :value="0" disabled>请选择实例</option><option v-for="instance in instances" :key="instance.id" :value="instance.id">{{ instance.host_name }} · {{ instance.version }} :{{ instance.port }}</option></select></label><label>Cron 表达式<input v-model.trim="backupForm.schedule" placeholder="0 2 * * *" required><small>五段格式：分 时 日 月 周，按控制台时区执行</small></label><label>保留天数<input v-model.number="backupForm.retention_days" type="number" min="0" max="3650"></label><label>保留份数<input v-model.number="backupForm.retention_count" type="number" min="0" max="10000"></label><label class="check-label"><input v-model="backupForm.all_databases" type="checkbox">备份全部数据库</label><label v-if="!backupForm.all_databases" class="span-2">数据库名称<textarea v-model="backupForm.databases" rows="3" placeholder="每行一个，也支持逗号分隔"></textarea></label><div class="form-actions span-2"><button class="button primary" :disabled="busy || !instances.length"><Plus />保存备份计划</button></div></form>
        </section>
        <section class="panel"><div class="panel-head"><div><p class="eyebrow">SCHEDULES</p><h3>备份计划</h3></div></div><div class="table-wrap"><table><thead><tr><th>计划</th><th>实例</th><th>调度</th><th>保留策略</th><th>状态</th><th>操作</th></tr></thead><tbody><tr v-for="plan in backupPlans" :key="plan.id"><td><strong>{{ plan.name }}</strong><small>{{ plan.owner_username }}</small></td><td>{{ plan.host_name }}<small>{{ plan.version }} :{{ plan.port }}</small></td><td class="mono">{{ plan.schedule }}</td><td>{{ plan.retention_days || '—' }} 天 / {{ plan.retention_count || '—' }} 份</td><td><span class="status" :class="plan.enabled ? 'success':'neutral'">{{ plan.enabled ? '启用':'停用' }}</span></td><td><div class="row-actions"><button v-if="canOperate" title="立即备份" @click="runBackupPlan(plan)"><Play /></button><button v-if="canOperate" class="danger" title="删除计划" @click="deleteBackupPlan(plan)"><Trash2 /></button></div></td></tr><tr v-if="!backupPlans.length"><td colspan="6" class="empty">暂无备份计划</td></tr></tbody></table></div></section>
        <section class="panel"><div class="panel-head"><div><p class="eyebrow">BACKUP HISTORY</p><h3>备份记录</h3></div></div><div class="table-wrap"><table><thead><tr><th>时间</th><th>计划</th><th>状态</th><th>大小</th><th>SHA-256</th><th>操作</th></tr></thead><tbody><tr v-for="item in backupRuns" :key="item.id"><td>{{ formatTime(item.started_at) }}<small>{{ item.message }}</small></td><td>{{ item.plan_name }}</td><td><span class="status" :class="statusClass(item.status)">{{ item.status }}</span></td><td>{{ formatBytes(item.size) }}</td><td class="mono digest">{{ item.sha256 || '—' }}</td><td><div class="row-actions"><button v-if="item.status === 'success'" title="下载备份" @click="downloadBackup(item)">↓</button><button v-if="canOperate && ['queued','running'].includes(item.status)" title="取消任务" @click="cancelBackupRun(item)"><Square /></button></div></td></tr><tr v-if="!backupRuns.length"><td colspan="6" class="empty">暂无备份记录</td></tr></tbody></table></div></section>
      </div>

      <div v-else-if="page === 'monitor'" class="page-stack">
        <section class="panel"><div class="panel-head"><div><p class="eyebrow">MYSQL HEALTH</p><h3>选择要检查的实例</h3></div><span class="safety-badge"><Activity />按需采集，不常驻远程 Agent</span></div><div class="card-grid"><article v-for="instance in instances" :key="instance.id" class="cluster-card"><div class="cluster-icon"><Database /></div><div><h3>{{ instance.host_name }} · {{ instance.version }} :{{ instance.port }}</h3><p>{{ instance.role }} · {{ instance.state }}</p><button class="button secondary compact-button" @click="loadMetrics(instance)"><Activity />采集健康指标</button></div></article><div v-if="!instances.length" class="empty-card">暂无由控制台管理的 MySQL 实例</div></div></section>
        <template v-if="monitorInstance?.metrics"><section class="metric-grid"><article><span class="metric-icon blue"><Gauge /></span><div><b>{{ Number(monitorInstance.metrics.cpu_percent || 0).toFixed(1) }}%</b><span>主机 CPU · Load {{ monitorInstance.metrics.load1 || 0 }}</span></div></article><article><span class="metric-icon teal"><Activity /></span><div><b>{{ monitorInstance.metrics.memory_used_mb || 0 }} MiB</b><span>内存 / 总量 {{ monitorInstance.metrics.memory_total_mb || 0 }} MiB</span></div></article><article><span class="metric-icon violet"><Database /></span><div><b>{{ monitorInstance.metrics.threads_connected || 0 }}</b><span>MySQL 当前连接 / 运行 {{ monitorInstance.metrics.threads_running || 0 }}</span></div></article><article><span class="metric-icon amber"><ShieldCheck /></span><div><b>{{ monitorInstance.metrics.mysql_up ? '正常' : '异常' }}</b><span>MySQL Uptime {{ monitorInstance.metrics.mysql_uptime || 0 }} 秒</span></div></article></section><section class="panel"><div class="panel-head"><div><p class="eyebrow">HEALTH SNAPSHOT</p><h3>{{ monitorInstance.host_name }} · MySQL 详细指标</h3></div><button class="button secondary compact-button" @click="loadMetrics(monitorInstance)"><RefreshCw />重新采集</button></div><div class="table-wrap"><table><thead><tr><th>指标</th><th>当前值</th><th>指标</th><th>当前值</th><th>指标</th><th>当前值</th></tr></thead><tbody><tr><td>最大连接数</td><td>{{ monitorInstance.metrics.max_connections || '—' }}</td><td>历史最大连接</td><td>{{ monitorInstance.metrics.max_used_connections || '—' }}</td><td>累计连接</td><td>{{ monitorInstance.metrics.connections || '—' }}</td></tr><tr><td>Queries</td><td>{{ monitorInstance.metrics.queries || '—' }}</td><td>Questions</td><td>{{ monitorInstance.metrics.questions || '—' }}</td><td>慢查询</td><td>{{ monitorInstance.metrics.slow_queries || '—' }}</td></tr><tr><td>接收字节</td><td>{{ formatBytes(monitorInstance.metrics.bytes_received) }}</td><td>发送字节</td><td>{{ formatBytes(monitorInstance.metrics.bytes_sent) }}</td><td>打开表</td><td>{{ monitorInstance.metrics.open_tables || '—' }}</td></tr><tr><td>复制 IO</td><td>{{ monitorInstance.metrics.replication_io || '—' }}</td><td>复制 SQL</td><td>{{ monitorInstance.metrics.replication_sql || '—' }}</td><td>复制延迟</td><td>{{ monitorInstance.metrics.replication_lag || '—' }}</td></tr><tr><td>磁盘可用</td><td>{{ monitorInstance.metrics.disk_free_mb || 0 }} MiB</td><td>Swap 已用</td><td>{{ monitorInstance.metrics.swap_used_mb || 0 }} MiB</td><td>异常连接</td><td>{{ monitorInstance.metrics.aborted_connects || 0 }}</td></tr></tbody></table></div></section><section class="panel"><div class="panel-head"><div><p class="eyebrow">RECENT SAMPLES</p><h3>最近采样</h3></div></div><div class="table-wrap"><table><thead><tr><th>时间</th><th>CPU</th><th>内存</th><th>连接</th><th>MySQL</th></tr></thead><tbody><tr v-for="sample in monitorHistory" :key="sample.id"><td>{{ formatTime(sample.collected_at) }}</td><td>{{ metricAt(sample, 'cpu_percent') }}%</td><td>{{ metricAt(sample, 'memory_used_mb') }} MiB</td><td>{{ metricAt(sample, 'threads_connected') }}</td><td>{{ metricAt(sample, 'mysql_up') ? '正常' : '异常' }}</td></tr><tr v-if="!monitorHistory.length"><td colspan="5" class="empty">暂无历史采样，点击“采集健康指标”开始记录</td></tr></tbody></table></div></section></template><section v-else class="empty-card">请选择一个实例采集 CPU、内存、磁盘、连接数、查询、慢查询、流量和复制状态。</section>
      </div>

      <div v-else-if="page === 'clusters'" class="page-stack"><section class="panel"><div class="panel-head"><div><p class="eyebrow">TOPOLOGY</p><h3>复制、MGR 与 Router</h3></div></div><div class="card-grid"><article v-for="cluster in clusters" :key="cluster.id" class="cluster-card"><div class="cluster-icon"><Network /></div><div><span class="status" :class="statusClass(cluster.state)">{{ cluster.state }}</span><h3>{{ cluster.name }}</h3><p>{{ cluster.type.toUpperCase() }} · {{ cluster.group_name || 'GTID Replication' }}</p><template v-if="cluster.router_enabled"><p><strong>Router · {{ cluster.router_cluster_name }}</strong></p><small class="mono">Classic RW :{{ cluster.router_rw_port }} · {{ cluster.router_endpoints.join(' / ') }}</small></template><small>{{ formatTime(cluster.created_at) }}</small></div></article><div v-if="!clusters.length" class="empty-card">暂无集群拓扑</div></div></section></div>

      <div v-else-if="page === 'jobs'" class="split-layout"><section class="panel job-list"><div class="panel-head"><div><p class="eyebrow">TASK HISTORY</p><h3>任务记录</h3></div></div><button v-for="job in jobs" :key="job.id" :class="{ selected: selectedJob?.id === job.id }" @click="openJob(job.id)"><span class="job-state" :class="statusClass(job.state)"></span><div><strong>{{ job.kind }}</strong><span class="mono">{{ job.id.slice(0, 12) }}</span></div><time>{{ formatTime(job.created_at) }}</time></button><div v-if="!jobs.length" class="empty">暂无任务</div></section><section class="terminal-panel"><div class="terminal-head"><div><span></span><span></span><span></span></div><p v-if="selectedJob"><strong>{{ selectedJob.kind }}</strong> / {{ selectedJob.id }}</p><button v-if="canOperate && selectedJob?.kind === 'deployment' && selectedJob?.state === 'needs_verification'" class="button secondary compact-button" @click="verifyJob(selectedJob)"><Activity />核实远端状态</button><button v-if="canOperate && selectedJob?.kind === 'deployment' && selectedJob?.state === 'failed'" class="button secondary compact-button" @click="retryJob(selectedJob)"><RefreshCw />从失败节点重试</button><button v-if="isAdmin && selectedJob?.kind === 'deployment' && selectedJob?.state === 'failed'" class="button danger compact-button" @click="previewFailedCleanup(selectedJob)"><TriangleAlert />清理失败安装</button><button v-if="isAdmin && ['failed','cleaned'].includes(selectedJob?.state)" class="button danger compact-button" @click="deleteJob(selectedJob)"><Trash2 />{{ selectedJob.state === 'cleaned' ? '删除已清理记录' : '删除失败记录' }}</button><span v-if="selectedJob" class="status" :class="statusClass(selectedJob.state)">{{ selectedJob.state }}</span></div><section v-if="pendingCleanup" class="cleanup-confirm"><TriangleAlert /><div><strong>永久清理失败安装</strong><p>预览任务 {{ pendingCleanup.preview_job_id.slice(0, 12) }} 成功后，输入 <code>{{ pendingCleanup.confirmation }}</code>。将清理全部部署节点上该端口的数据库残留，并删除未被其他 AIM 实例使用的 MySQL 二进制目录。</p><input v-model.trim="cleanupConfirmationInput" :placeholder="pendingCleanup.confirmation"><button class="button danger compact-button" @click="confirmFailedCleanup">确认清理</button><button class="button ghost compact-button" @click="pendingCleanup=null">取消</button></div></section><div class="terminal-body" aria-live="polite"><template v-if="selectedJob"><p v-for="log in jobLogs" :key="log.id" :class="`log-${log.level}`"><time>{{ new Date(log.created_at).toLocaleTimeString() }}</time><b>[{{ log.phase }}]</b><span>{{ log.message }}</span></p><p v-if="!jobLogs.length" class="terminal-empty">等待任务日志…</p></template><div v-else class="terminal-placeholder"><TerminalSquare /><p>选择左侧任务查看实时执行日志</p></div></div></section></div>

      <div v-else-if="page === 'secrets'" class="page-stack"><section class="panel"><div class="panel-head"><div><p class="eyebrow">ENCRYPTED VAULT</p><h3>密码保险箱</h3></div><span class="safety-badge"><ShieldCheck />AES-256-GCM</span></div><div class="table-wrap"><table><thead><tr><th>名称</th><th>类型</th><th>创建时间</th><th></th></tr></thead><tbody><tr v-for="secret in secrets" :key="secret.id"><td><strong>{{ secret.name }}</strong></td><td>{{ secret.kind }}</td><td>{{ formatTime(secret.created_at) }}</td><td><button class="button secondary compact-button" @click="revealSecret(secret)"><Eye />查看并审计</button></td></tr><tr v-if="!secrets.length"><td colspan="4" class="empty">暂无加密密码</td></tr></tbody></table></div></section><section v-if="revealedSecret" class="secret-reveal"><KeyRound /><div><span>{{ revealedSecret.name }}</span><code>{{ revealedSecret.value }}</code><small>本次查看操作已写入审计日志。</small></div><button @click="revealedSecret=null">×</button></section></div>

      <div v-else-if="page === 'users'" class="page-stack"><section class="panel"><div class="panel-head"><div><p class="eyebrow">LOCAL RBAC</p><h3>创建本地用户</h3></div></div><form class="form-grid inline-form" @submit.prevent="createUser"><label>用户名<input v-model.trim="userForm.username" required></label><label>初始密码<input v-model="userForm.password" type="password" minlength="12" required></label><label>角色<select v-model="userForm.role"><option value="viewer">只读用户</option><option value="operator">操作员</option><option value="admin">管理员</option></select></label><button class="button primary"><Plus />创建用户</button></form></section><section class="panel"><div class="table-wrap"><table><thead><tr><th>用户名</th><th>角色</th><th>状态</th><th></th></tr></thead><tbody><tr v-for="item in users" :key="item.id"><td><strong>{{ item.username }}</strong></td><td>{{ item.role }}</td><td><span class="status" :class="item.active ? 'success':'neutral'">{{ item.active ? 'active':'disabled' }}</span></td><td><button v-if="item.id !== user.id" class="button secondary compact-button" @click="toggleUser(item)">{{ item.active ? '停用' : '启用' }}</button></td></tr></tbody></table></div></section></div>

      <div v-else-if="page === 'audit'" class="page-stack"><section class="panel"><div class="panel-head"><div><p class="eyebrow">IMMUTABLE TRAIL</p><h3>最近 500 条审计事件</h3></div></div><div class="table-wrap"><table><thead><tr><th>时间</th><th>用户 / 来源</th><th>动作</th><th>对象</th><th>详情</th></tr></thead><tbody><tr v-for="item in audits" :key="item.id"><td>{{ formatTime(item.created_at) }}</td><td><strong>{{ item.username }}</strong><small>{{ item.remote_addr }}</small></td><td class="mono">{{ item.action }}</td><td>{{ item.object_type }} / {{ item.object_id || '—' }}</td><td class="mono digest">{{ item.detail_json }}</td></tr></tbody></table></div></section></div>
    </section>
  </div>
</template>
