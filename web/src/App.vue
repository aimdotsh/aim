<script setup>
import { computed, onMounted, reactive, ref, watch } from 'vue'
import {
  Activity, Archive, Boxes, ChevronRight, CircleUserRound, Database, Eye, FileClock,
  Gauge, HardDriveUpload, KeyRound, LayoutDashboard, LogOut, Network, Play, Plus,
  RefreshCw, Server, ShieldCheck, Square, TerminalSquare, Trash2, TriangleAlert, Users
} from '@lucide/vue'
import { api, csrfToken, formatBytes, formatTime } from './api'

const user = ref(null)
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
const users = ref([])
const audits = ref([])
const secrets = ref([])
const selectedJob = ref(null)
const jobLogs = ref([])
const uploadProgress = ref(0)
const revealedSecret = ref(null)
let eventSource

const loginForm = reactive({ username: '', password: '' })
const hostForm = reactive({ name: '', address: '', ssh_port: 22, ssh_user: 'aimops', private_key: '' })
const userForm = reactive({ username: '', password: '', role: 'viewer' })
const deploy = reactive({
  name: '', mode: 'standalone', version: '8.0.46', port: 3306, bind_address: '0.0.0.0', media_id: 0,
  mgr_port: 33061, mgr_allowlist: '', mgr_group_name: '', mgr_recovery_user: 'aim_mgr',
  replication_user: 'aim_repl', source_host: '', source_port: 3306, source_password: '', replica_host: '%',
  root_password: '', replication_password: '', mgr_recovery_password: '', nodes: [{ host_id: 0, local_ip: '', server_id: 0 }]
})
const pendingDestructive = ref(null)
const confirmation = ref('')

const navigation = computed(() => [
  { id: 'dashboard', label: '运行总览', icon: LayoutDashboard },
  { id: 'hosts', label: '主机资源', icon: Server },
  { id: 'media', label: '安装介质', icon: Archive },
  { id: 'deploy', label: '部署向导', icon: HardDriveUpload, roles: ['admin', 'operator'] },
  { id: 'instances', label: 'MySQL 实例', icon: Database },
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
  deploy.nodes = Array.from({ length: count }, (_, index) => deploy.nodes[index] || { host_id: 0, local_ip: '', server_id: 0 })
})

watch([() => deploy.version, () => deploy.nodes.map(node => node.host_id).join(','), compatibleMedia], () => {
  if (!compatibleMedia.value.some(item => item.id === Number(deploy.media_id))) {
    deploy.media_id = compatibleMedia.value[0]?.id || 0
  }
})

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

async function logout() {
  await api('/session', { method: 'DELETE' }).catch(() => {})
  user.value = null
  csrfToken.value = ''
  if (eventSource) eventSource.close()
}

async function refreshAll() {
  const requests = [
    api('/dashboard'), api('/hosts'), api('/media'), api('/jobs'), api('/instances'), api('/clusters')
  ]
  const [dash, hostList, mediaList, jobList, instanceList, clusterList] = await Promise.all(requests)
  dashboard.value = dash
  hosts.value = hostList
  media.value = mediaList
  jobs.value = jobList
  instances.value = instanceList
  clusters.value = clusterList
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
    hosts.value = await api('/hosts')
    flash('主机已保存，请确认 SSH 指纹后执行探测')
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
    await api(`/hosts/${host.id}/probe`, { method: 'POST', body: JSON.stringify({ ports: [3306, 8023, 33061, 18023] }) })
    hosts.value = await api('/hosts')
    flash(`${host.name} 探测完成`)
  }).catch(() => {})
}

async function uploadMedia(event) {
  const file = event.target.files?.[0]
  if (!file) return
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
    event.target.value = ''
  }).catch(() => {})
}

function hostIPs(hostID) {
  return hosts.value.find(host => host.id === Number(hostID))?.facts?.ipv4 || []
}

async function createDeployment() {
  await run(async () => {
    const payload = JSON.parse(JSON.stringify(deploy))
    payload.media_id = Number(payload.media_id) || 0
    payload.port = Number(payload.port)
    payload.mgr_port = Number(payload.mgr_port)
    payload.source_port = Number(payload.source_port)
    payload.nodes = payload.nodes.map(node => ({ host_id: Number(node.host_id), local_ip: node.local_ip, server_id: Number(node.server_id) || 0 }))
    const result = await api('/deployments', { method: 'POST', body: JSON.stringify(payload) })
    flash(`部署任务 ${result.job_id.slice(0, 8)} 已进入队列`)
    page.value = 'jobs'
    jobs.value = await api('/jobs')
    openJob(result.job_id)
  }).catch(() => {})
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

async function verifyJob(job) {
  await run(async () => {
    await api(`/jobs/${job.id}/verify`, { method: 'POST', body: '{}' })
    jobs.value = await api('/jobs')
    selectedJob.value = await api(`/jobs/${job.id}`)
    flash('远端状态已核实，现在可以从未完成节点重试')
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
  if (['online', 'running', 'complete'].includes(state)) return 'success'
  if (['failed', 'error', 'missing'].includes(state)) return 'danger'
  if (['queued', 'preflight', 'transferring', 'running', 'pending'].includes(state)) return 'warning'
  return 'neutral'
}

onMounted(restoreSession)
</script>

<template>
  <div v-if="loading" class="loading-screen"><div class="brand-mark">aim<span>.sh</span></div><p>正在连接控制台…</p></div>

  <main v-else-if="!user" class="login-page">
    <section class="login-story">
      <div class="brand-mark light">aim<span>.sh</span></div>
      <p class="eyebrow">MYSQL DEPLOYMENT CONTROL PLANE</p>
      <h1>让数据库部署变成<br><em>可验证的标准流程</em></h1>
      <p class="story-copy">从系统探测、安装包校验到单机、主从与三节点 MGR，在一处完成编排、审计和生命周期管理。</p>
      <div class="story-grid">
        <div><ShieldCheck /><strong>受限执行</strong><span>固定 SSH 指纹与最小 sudo 权限</span></div>
        <div><Activity /><strong>实时可见</strong><span>部署阶段与远程日志全程留痕</span></div>
        <div><Boxes /><strong>全系兼容</strong><span>MySQL 5.6 / 5.7 / 8.0 / 8.4</span></div>
      </div>
    </section>
    <section class="login-panel">
      <form class="login-card" @submit.prevent="login">
        <p class="eyebrow">AIM CONSOLE</p><h2>登录内网控制台</h2><p>使用管理员分配的本地账号继续。</p>
        <label>用户名<input v-model.trim="loginForm.username" autocomplete="username" required autofocus></label>
        <label>密码<input v-model="loginForm.password" type="password" autocomplete="current-password" required></label>
        <div v-if="error" class="alert danger"><TriangleAlert />{{ error }}</div>
        <button class="button primary wide" :disabled="busy"><span>{{ busy ? '正在验证…' : '安全登录' }}</span><ChevronRight /></button>
      </form>
    </section>
  </main>

  <div v-else class="app-shell">
    <aside class="sidebar">
      <div class="brand-mark light compact">aim<span>.sh</span></div>
      <div class="environment"><span></span>内网控制平面</div>
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
        <section v-if="isAdmin" class="panel"><div class="panel-head"><div><p class="eyebrow">HOST ONBOARDING</p><h3>添加受管主机</h3></div></div>
          <form class="form-grid host-form" @submit.prevent="createHost"><label>主机名称<input v-model.trim="hostForm.name" placeholder="node00" required></label><label>IP 或域名<input v-model.trim="hostForm.address" placeholder="172.20.23.90" required></label><label>SSH 端口<input v-model.number="hostForm.ssh_port" type="number" min="1" max="65535" required></label><label>SSH 用户<input v-model.trim="hostForm.ssh_user" required></label><label class="span-2">专用 SSH 私钥<textarea v-model="hostForm.private_key" rows="4" placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" required></textarea></label><div class="form-actions span-2"><button class="button primary" :disabled="busy"><Plus />保存主机</button></div></form>
        </section>
        <section class="panel"><div class="panel-head"><div><p class="eyebrow">INVENTORY</p><h3>主机资源</h3></div></div>
          <div class="card-grid"><article v-for="host in hosts" :key="host.id" class="host-card"><div class="host-card-head"><span class="server-glyph"><Server /></span><div><h4>{{ host.name }}</h4><span class="mono">{{ host.address }}:{{ host.ssh_port }}</span></div><span class="status" :class="statusClass(host.status)">{{ host.status }}</span></div><dl><div><dt>系统</dt><dd>{{ host.facts?.os_name || '等待探测' }}</dd></div><div><dt>架构 / glibc</dt><dd>{{ host.facts?.architecture || '—' }} / {{ host.facts?.glibc || '—' }}</dd></div><div><dt>IPv4</dt><dd>{{ host.facts?.ipv4?.join(', ') || '—' }}</dd></div><div><dt>资源</dt><dd>{{ host.facts?.cpus || '—' }} CPU · {{ host.facts?.memory_mb || '—' }} MiB</dd></div></dl><div class="card-actions"><button v-if="isAdmin && !host.host_key_fingerprint" class="button secondary" @click="trustFingerprint(host)"><KeyRound />确认指纹</button><button v-if="canOperate && host.host_key_fingerprint" class="button secondary" @click="probeHost(host)"><Activity />重新探测</button></div><p v-if="host.last_error" class="inline-error">{{ host.last_error }}</p></article><div v-if="!hosts.length" class="empty-card">还没有受管主机，请先添加专用 aimops SSH 账号。</div></div>
        </section>
      </div>

      <div v-else-if="page === 'media'" class="page-stack">
        <section v-if="canOperate" class="upload-zone"><Archive /><div><h3>上传 MySQL Generic 安装包</h3><p>支持 .tar.xz、.tar.gz、.tgz 和 .tar，最大 2 GiB；自动分块续传并计算 SHA-256。</p></div><label class="button primary">选择软件包<input type="file" accept=".xz,.gz,.tgz,.tar" hidden @change="uploadMedia"></label><progress v-if="uploadProgress" :value="uploadProgress" max="100">{{ uploadProgress }}%</progress></section>
        <section class="panel"><div class="panel-head"><div><p class="eyebrow">PACKAGE LIBRARY</p><h3>安装介质库</h3></div></div><div class="table-wrap"><table><thead><tr><th>文件名</th><th>版本</th><th>glibc / 架构</th><th>大小</th><th>SHA-256</th></tr></thead><tbody><tr v-for="item in media" :key="item.id"><td><strong>{{ item.filename }}</strong><small v-if="item.minimal">minimal</small></td><td>{{ item.version }}</td><td>{{ item.glibc }} / {{ item.architecture }}</td><td>{{ formatBytes(item.size) }}</td><td class="mono digest">{{ item.sha256 }}</td></tr><tr v-if="!media.length"><td colspan="5" class="empty">暂无安装介质，也可以在部署时选择由目标机官方下载。</td></tr></tbody></table></div></section>
      </div>

      <div v-else-if="page === 'deploy'" class="page-stack">
        <section class="panel wizard"><div class="panel-head"><div><p class="eyebrow">DEPLOYMENT WIZARD</p><h3>创建 MySQL 部署任务</h3></div><span class="safety-badge"><ShieldCheck />先整体预检，再顺序执行</span></div>
          <form @submit.prevent="createDeployment">
            <fieldset><legend><span>01</span>基础规格</legend><div class="form-grid"><label>部署名称<input v-model.trim="deploy.name" placeholder="production-mysql" required></label><label>部署模式<select v-model="deploy.mode"><option value="standalone">单机实例</option><option value="source">仅主库</option><option value="replica">仅从库</option><option value="replication">一主一从</option><option value="mgr">三节点 MGR</option></select></label><label>MySQL 版本<input v-model.trim="deploy.version" placeholder="8.0.46" required></label><label>SQL 端口<input v-model.number="deploy.port" type="number" min="1" max="65535" required></label><label>绑定地址<input v-model.trim="deploy.bind_address" required></label><label>安装介质<select v-model.number="deploy.media_id"><option :value="0">目标机官方下载</option><option v-for="item in compatibleMedia" :key="item.id" :value="item.id">{{ item.filename }}</option></select><small v-if="compatibleMedia.length">已按所选主机的 glibc 与架构自动匹配</small><small v-else>暂无兼容介质，将由目标机官方下载</small></label></div></fieldset>
            <fieldset><legend><span>02</span>节点与网络</legend><div class="node-grid"><article v-for="(node, index) in deploy.nodes" :key="index"><div class="node-number">{{ String(index + 1).padStart(2, '0') }}</div><label>目标主机<select v-model.number="node.host_id" required><option :value="0" disabled>请选择</option><option v-for="host in hosts.filter(h => h.status === 'online')" :key="host.id" :value="host.id">{{ host.name }} · {{ host.address }}</option></select></label><label v-if="deploy.mode === 'mgr' || deploy.mode === 'replication'">业务网 IP<select v-model="node.local_ip" required><option value="" disabled>请选择探测到的 IP</option><option v-for="ip in hostIPs(node.host_id)" :key="ip">{{ ip }}</option></select></label><label v-if="deploy.mode !== 'standalone'">server_id<input v-model.number="node.server_id" type="number" min="1" max="4294967294" required></label><span v-if="deploy.mode === 'mgr'" class="node-role">{{ index === 0 ? 'BOOTSTRAP' : 'JOIN' }}</span></article></div></fieldset>
            <fieldset v-if="deploy.mode === 'mgr'"><legend><span>03</span>MGR 参数</legend><div class="form-grid"><label>MGR 通信端口<input v-model.number="deploy.mgr_port" type="number" min="1" max="65535" required></label><label>IP allowlist（可自动生成）<input v-model.trim="deploy.mgr_allowlist" placeholder="留空使用三个节点的精确 IP"></label><label>组 UUID（可自动生成）<input v-model.trim="deploy.mgr_group_name" placeholder="留空自动生成"></label><label>恢复账号<input v-model.trim="deploy.mgr_recovery_user"></label><label class="span-2">共享恢复密码（可自动生成）<input v-model="deploy.mgr_recovery_password" type="password" autocomplete="new-password" placeholder="留空由控制台生成"></label></div></fieldset>
            <fieldset v-if="['source','replica','replication'].includes(deploy.mode)"><legend><span>03</span>复制参数</legend><div class="form-grid"><label>复制账号<input v-model.trim="deploy.replication_user"></label><label v-if="deploy.mode === 'source'">允许的从库地址<input v-model.trim="deploy.replica_host"></label><label v-if="deploy.mode === 'replica'">源库 IP<input v-model.trim="deploy.source_host" required></label><label v-if="deploy.mode === 'replica'">源库端口<input v-model.number="deploy.source_port" type="number" required></label><label v-if="deploy.mode === 'replica'" class="span-2">源库复制密码<input v-model="deploy.source_password" type="password" required></label><label v-else class="span-2">复制密码（可自动生成）<input v-model="deploy.replication_password" type="password" placeholder="留空由控制台生成"></label></div></fieldset>
            <fieldset><legend><span>{{ deploy.mode === 'mgr' || ['source','replica','replication'].includes(deploy.mode) ? '04' : '03' }}</span>凭据</legend><div class="form-grid"><label class="span-2">MySQL root 密码（可自动生成）<input v-model="deploy.root_password" type="password" autocomplete="new-password" placeholder="留空由控制台生成并加密保存"></label></div></fieldset>
            <div class="wizard-footer"><div><ShieldCheck /><p><strong>提交后不会立即盲目安装</strong><span>控制台会先验证主机、端口、glibc、架构和安装包。</span></p></div><button class="button primary large" :disabled="busy || !hosts.length"><Play />创建部署任务</button></div>
          </form>
        </section>
      </div>

      <div v-else-if="page === 'instances'" class="page-stack"><section class="panel"><div class="panel-head"><div><p class="eyebrow">INSTANCE LIFECYCLE</p><h3>MySQL 实例</h3></div></div><div class="table-wrap"><table><thead><tr><th>主机</th><th>实例</th><th>角色</th><th>状态</th><th>操作</th></tr></thead><tbody><tr v-for="instance in instances" :key="instance.id"><td><strong>{{ instance.host_name }}</strong><small>{{ instance.address }}</small></td><td><span class="mono">{{ instance.version }} :{{ instance.port }}</span></td><td>{{ instance.role }}</td><td><span class="status" :class="statusClass(instance.state)">{{ instance.state }}</span></td><td><div v-if="canOperate" class="row-actions"><button title="启动" @click="instanceAction(instance,'start')"><Play /></button><button title="停止" @click="instanceAction(instance,'stop')"><Square /></button><button title="状态检查" @click="instanceAction(instance,'status')"><Activity /></button><button v-if="isAdmin" class="warning" title="重新初始化" @click="previewDestructive(instance,'reinitialize')"><RefreshCw /></button><button v-if="isAdmin" class="danger" title="卸载" @click="previewDestructive(instance,'uninstall')"><Trash2 /></button></div></td></tr><tr v-if="!instances.length"><td colspan="5" class="empty">暂无由控制台管理的实例</td></tr></tbody></table></div></section>
        <section v-if="pendingDestructive" class="danger-panel"><TriangleAlert /><div><h3>{{ pendingDestructive.action === 'uninstall' ? '永久卸载实例' : '重新初始化实例' }}</h3><p>预览任务：<button class="mono-link" @click="page='jobs'; openJob(pendingDestructive.preview_job_id)">{{ pendingDestructive.preview_job_id }}</button></p><p>预览成功后，输入 <strong>{{ pendingDestructive.instance.address }}:{{ pendingDestructive.instance.port }}</strong> 才能执行。</p><input v-model.trim="confirmation" :placeholder="`${pendingDestructive.instance.address}:${pendingDestructive.instance.port}`"><div class="card-actions"><button class="button danger" @click="confirmDestructive">确认执行</button><button class="button ghost" @click="pendingDestructive=null">取消</button></div></div></section>
      </div>

      <div v-else-if="page === 'clusters'" class="page-stack"><section class="panel"><div class="panel-head"><div><p class="eyebrow">TOPOLOGY</p><h3>复制与 MGR 集群</h3></div></div><div class="card-grid"><article v-for="cluster in clusters" :key="cluster.id" class="cluster-card"><div class="cluster-icon"><Network /></div><div><span class="status" :class="statusClass(cluster.state)">{{ cluster.state }}</span><h3>{{ cluster.name }}</h3><p>{{ cluster.type.toUpperCase() }} · {{ cluster.group_name || 'GTID Replication' }}</p><small>{{ formatTime(cluster.created_at) }}</small></div></article><div v-if="!clusters.length" class="empty-card">暂无集群拓扑</div></div></section></div>

      <div v-else-if="page === 'jobs'" class="split-layout"><section class="panel job-list"><div class="panel-head"><div><p class="eyebrow">TASK HISTORY</p><h3>任务记录</h3></div></div><button v-for="job in jobs" :key="job.id" :class="{ selected: selectedJob?.id === job.id }" @click="openJob(job.id)"><span class="job-state" :class="statusClass(job.state)"></span><div><strong>{{ job.kind }}</strong><span class="mono">{{ job.id.slice(0, 12) }}</span></div><time>{{ formatTime(job.created_at) }}</time></button><div v-if="!jobs.length" class="empty">暂无任务</div></section><section class="terminal-panel"><div class="terminal-head"><div><span></span><span></span><span></span></div><p v-if="selectedJob"><strong>{{ selectedJob.kind }}</strong> / {{ selectedJob.id }}</p><button v-if="canOperate && selectedJob?.kind === 'deployment' && selectedJob?.state === 'needs_verification'" class="button secondary compact-button" @click="verifyJob(selectedJob)"><Activity />核实远端状态</button><button v-if="canOperate && selectedJob?.kind === 'deployment' && selectedJob?.state === 'failed'" class="button secondary compact-button" @click="retryJob(selectedJob)"><RefreshCw />从失败节点重试</button><span v-if="selectedJob" class="status" :class="statusClass(selectedJob.state)">{{ selectedJob.state }}</span></div><div class="terminal-body" aria-live="polite"><template v-if="selectedJob"><p v-for="log in jobLogs" :key="log.id" :class="`log-${log.level}`"><time>{{ new Date(log.created_at).toLocaleTimeString() }}</time><b>[{{ log.phase }}]</b><span>{{ log.message }}</span></p><p v-if="!jobLogs.length" class="terminal-empty">等待任务日志…</p></template><div v-else class="terminal-placeholder"><TerminalSquare /><p>选择左侧任务查看实时执行日志</p></div></div></section></div>

      <div v-else-if="page === 'secrets'" class="page-stack"><section class="panel"><div class="panel-head"><div><p class="eyebrow">ENCRYPTED VAULT</p><h3>密码保险箱</h3></div><span class="safety-badge"><ShieldCheck />AES-256-GCM</span></div><div class="table-wrap"><table><thead><tr><th>名称</th><th>类型</th><th>创建时间</th><th></th></tr></thead><tbody><tr v-for="secret in secrets" :key="secret.id"><td><strong>{{ secret.name }}</strong></td><td>{{ secret.kind }}</td><td>{{ formatTime(secret.created_at) }}</td><td><button class="button secondary compact-button" @click="revealSecret(secret)"><Eye />查看并审计</button></td></tr><tr v-if="!secrets.length"><td colspan="4" class="empty">暂无加密密码</td></tr></tbody></table></div></section><section v-if="revealedSecret" class="secret-reveal"><KeyRound /><div><span>{{ revealedSecret.name }}</span><code>{{ revealedSecret.value }}</code><small>本次查看操作已写入审计日志。</small></div><button @click="revealedSecret=null">×</button></section></div>

      <div v-else-if="page === 'users'" class="page-stack"><section class="panel"><div class="panel-head"><div><p class="eyebrow">LOCAL RBAC</p><h3>创建本地用户</h3></div></div><form class="form-grid inline-form" @submit.prevent="createUser"><label>用户名<input v-model.trim="userForm.username" required></label><label>初始密码<input v-model="userForm.password" type="password" minlength="12" required></label><label>角色<select v-model="userForm.role"><option value="viewer">只读用户</option><option value="operator">操作员</option><option value="admin">管理员</option></select></label><button class="button primary"><Plus />创建用户</button></form></section><section class="panel"><div class="table-wrap"><table><thead><tr><th>用户名</th><th>角色</th><th>状态</th><th></th></tr></thead><tbody><tr v-for="item in users" :key="item.id"><td><strong>{{ item.username }}</strong></td><td>{{ item.role }}</td><td><span class="status" :class="item.active ? 'success':'neutral'">{{ item.active ? 'active':'disabled' }}</span></td><td><button v-if="item.id !== user.id" class="button secondary compact-button" @click="toggleUser(item)">{{ item.active ? '停用' : '启用' }}</button></td></tr></tbody></table></div></section></div>

      <div v-else-if="page === 'audit'" class="page-stack"><section class="panel"><div class="panel-head"><div><p class="eyebrow">IMMUTABLE TRAIL</p><h3>最近 500 条审计事件</h3></div></div><div class="table-wrap"><table><thead><tr><th>时间</th><th>用户 / 来源</th><th>动作</th><th>对象</th><th>详情</th></tr></thead><tbody><tr v-for="item in audits" :key="item.id"><td>{{ formatTime(item.created_at) }}</td><td><strong>{{ item.username }}</strong><small>{{ item.remote_addr }}</small></td><td class="mono">{{ item.action }}</td><td>{{ item.object_type }} / {{ item.object_id || '—' }}</td><td class="mono digest">{{ item.detail_json }}</td></tr></tbody></table></div></section></div>
    </section>
  </div>
</template>
