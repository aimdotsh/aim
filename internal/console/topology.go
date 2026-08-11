package console

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/aimdotsh/aim/internal/executor"
)

type storedTopologyInstance struct {
	id        int64
	hostID    int64
	hostName  string
	address   string
	factsJSON string
	port      int
	role      string
	clusterID sql.NullInt64
	specJSON  string
}

func instanceAddresses(address, factsJSON, specJSON string, hostID int64) []string {
	seen := map[string]bool{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if net.ParseIP(value) != nil {
			seen[value] = true
		}
	}
	var spec DeploymentRequest
	if json.Unmarshal([]byte(specJSON), &spec) == nil {
		for _, node := range spec.Nodes {
			if node.HostID == hostID {
				add(node.LocalIP)
			}
		}
	}
	add(address)
	var facts executor.HostFacts
	if json.Unmarshal([]byte(factsJSON), &facts) == nil {
		for _, value := range facts.IPv4 {
			add(value)
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	return result
}

func preferredInstanceAddress(address, factsJSON, specJSON string, hostID int64) string {
	var spec DeploymentRequest
	if json.Unmarshal([]byte(specJSON), &spec) == nil {
		for _, node := range spec.Nodes {
			if node.HostID == hostID && net.ParseIP(node.LocalIP) != nil {
				return node.LocalIP
			}
		}
	}
	if net.ParseIP(address) != nil {
		return address
	}
	var facts executor.HostFacts
	if json.Unmarshal([]byte(factsJSON), &facts) == nil {
		for _, value := range facts.IPv4 {
			if net.ParseIP(value) != nil {
				return value
			}
		}
	}
	return address
}

// reconcileReplicationTopologies links historical replica-only deployments to
// their managed source when the source endpoint identifies exactly one source.
// Ambiguous or external sources are intentionally left untouched.
func (s *Store) reconcileReplicationTopologies(ctx context.Context) (int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT i.id,i.host_id,h.name,h.address,h.facts_json,i.port,i.role,i.cluster_id,i.spec_json
		FROM instances i JOIN hosts h ON h.id=i.host_id ORDER BY i.id`)
	if err != nil {
		return 0, err
	}
	instances := []*storedTopologyInstance{}
	for rows.Next() {
		item := &storedTopologyInstance{}
		if err := rows.Scan(&item.id, &item.hostID, &item.hostName, &item.address, &item.factsJSON, &item.port, &item.role, &item.clusterID, &item.specJSON); err != nil {
			rows.Close()
			return 0, err
		}
		instances = append(instances, item)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	linked := 0
	for _, replica := range instances {
		if replica.role != "replica" || replica.clusterID.Valid {
			continue
		}
		var replicaSpec DeploymentRequest
		if json.Unmarshal([]byte(replica.specJSON), &replicaSpec) != nil || net.ParseIP(replicaSpec.SourceHost) == nil || replicaSpec.SourcePort < 1 {
			continue
		}
		candidates := []*storedTopologyInstance{}
		for _, source := range instances {
			if source.role != "source" || source.port != replicaSpec.SourcePort {
				continue
			}
			for _, candidateAddress := range instanceAddresses(source.address, source.factsJSON, source.specJSON, source.hostID) {
				if candidateAddress == replicaSpec.SourceHost {
					candidates = append(candidates, source)
					break
				}
			}
		}
		if len(candidates) != 1 {
			continue
		}
		source := candidates[0]
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return linked, err
		}
		clusterID := int64(0)
		if source.clusterID.Valid {
			var clusterType string
			if err := tx.QueryRowContext(ctx, `SELECT type FROM clusters WHERE id=?`, source.clusterID.Int64).Scan(&clusterType); err != nil || clusterType != "replication" {
				tx.Rollback()
				continue
			}
			clusterID = source.clusterID.Int64
		} else {
			name := topologyDeploymentName(source.specJSON, source.hostName, source.port)
			now := time.Now().UTC().Format(time.RFC3339Nano)
			result, err := tx.ExecContext(ctx, `INSERT INTO clusters(name,type,group_name,state,created_at,updated_at) VALUES(?, 'replication', '', 'online', ?, ?)`, name, now, now)
			if err != nil {
				tx.Rollback()
				return linked, err
			}
			clusterID, _ = result.LastInsertId()
			if _, err := tx.ExecContext(ctx, `UPDATE instances SET cluster_id=?,updated_at=? WHERE id=? AND cluster_id IS NULL`, clusterID, now, source.id); err != nil {
				tx.Rollback()
				return linked, err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE instances SET cluster_id=?,updated_at=? WHERE id=? AND cluster_id IS NULL`, clusterID, time.Now().UTC().Format(time.RFC3339Nano), replica.id); err != nil {
			tx.Rollback()
			return linked, err
		}
		if err := tx.Commit(); err != nil {
			return linked, err
		}
		source.clusterID = sql.NullInt64{Int64: clusterID, Valid: true}
		replica.clusterID = sql.NullInt64{Int64: clusterID, Valid: true}
		linked++
	}
	return linked, nil
}

func topologyDeploymentName(specJSON, hostName string, port int) string {
	var spec DeploymentRequest
	if json.Unmarshal([]byte(specJSON), &spec) == nil && strings.TrimSpace(spec.Name) != "" {
		return strings.TrimSpace(spec.Name)
	}
	return fmt.Sprintf("%s:%d", hostName, port)
}
