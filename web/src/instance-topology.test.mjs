import assert from 'node:assert/strict'
import test from 'node:test'
import { groupInstances, managedSourceInstances } from './instance-topology.js'

const instances = [
  { id: 1, cluster_id: 10, topology_key: 'cluster:10', topology_name: 'mgr-prod', topology_type: 'mgr', host_name: 'mgr002', port: 3316, role: 'mgr', state: 'running' },
  { id: 2, cluster_id: 11, topology_key: 'cluster:11', topology_name: 'primary-replica', topology_type: 'replication', host_name: 'mgr002', port: 3326, role: 'replica', state: 'running' },
  { id: 3, cluster_id: 10, topology_key: 'cluster:10', topology_name: 'mgr-prod', topology_type: 'mgr', host_name: 'mgr001', port: 3316, role: 'mgr', state: 'running' },
  { id: 4, cluster_id: 11, topology_key: 'cluster:11', topology_name: 'primary-replica', topology_type: 'replication', host_name: 'mgr001', port: 3326, role: 'source', state: 'running', source_address: '10.0.0.11' },
  { id: 5, topology_key: 'instance:5', topology_name: 'standalone-lab', topology_type: 'standalone', host_name: 'mgr003', host_id: 3, port: 3336, role: 'standalone', state: 'stopped' },
  { id: 6, cluster_id: 10, topology_key: 'cluster:10', topology_name: 'mgr-prod', topology_type: 'mgr', host_name: 'mgr003', port: 3316, role: 'mgr', state: 'running' }
]

test('instances are grouped and ordered by deployment topology', () => {
  const groups = groupInstances(instances)
  assert.deepEqual(groups.map(group => [group.type, group.instances.length]), [['mgr', 3], ['replication', 2], ['standalone', 1]])
  assert.deepEqual(groups[1].instances.map(instance => instance.role), ['source', 'replica'])
})

test('running managed instances can populate a replica source', () => {
  const choices = managedSourceInstances(instances, 3, 3336)
  assert.equal(choices.some(instance => instance.state === 'stopped'), false)
  assert.equal(choices.find(instance => instance.id === 4).source_address, '10.0.0.11')
})
