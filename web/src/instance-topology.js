const topologyPriority = { mgr: 0, replication: 1, source: 2, replica: 3, standalone: 4 }
const rolePriority = { source: 0, replica: 1, mgr: 2, standalone: 3 }

export function topologyTypeLabel(type) {
  return ({ mgr: 'MGR 集群', replication: '主从复制', source: '独立主库', replica: '独立从库', standalone: '单机实例' })[type] || 'MySQL 拓扑'
}

export function groupInstances(instances) {
  const grouped = new Map()
  for (const instance of instances || []) {
    const key = instance.topology_key || (Number(instance.cluster_id) > 0 ? `cluster:${instance.cluster_id}` : `instance:${instance.id}`)
    if (!grouped.has(key)) {
      grouped.set(key, {
        key,
        name: instance.topology_name || instance.cluster_name || `${instance.host_name}:${instance.port}`,
        type: instance.topology_type || instance.cluster_type || instance.role || 'standalone',
        created_at: instance.created_at || '',
        instances: []
      })
    }
    grouped.get(key).instances.push(instance)
  }
  const groups = Array.from(grouped.values())
  for (const group of groups) {
    group.instances.sort((left, right) =>
      (rolePriority[left.role] ?? 9) - (rolePriority[right.role] ?? 9) ||
      String(left.host_name || '').localeCompare(String(right.host_name || ''), 'zh-CN') ||
      Number(left.port) - Number(right.port))
  }
  return groups.sort((left, right) =>
    (topologyPriority[left.type] ?? 9) - (topologyPriority[right.type] ?? 9) ||
    String(left.name || '').localeCompare(String(right.name || ''), 'zh-CN') ||
    String(left.created_at).localeCompare(String(right.created_at)))
}

export function managedSourceInstances(instances, targetHostID = 0, targetPort = 0) {
  return (instances || []).filter(instance => {
    const state = String(instance.state || '').toLowerCase()
    if (!['running', 'online'].includes(state)) return false
    return Number(instance.host_id) !== Number(targetHostID) || Number(instance.port) !== Number(targetPort)
  }).sort((left, right) =>
    (rolePriority[left.role] ?? 9) - (rolePriority[right.role] ?? 9) ||
    String(left.host_name || '').localeCompare(String(right.host_name || ''), 'zh-CN') ||
    Number(left.port) - Number(right.port))
}
