package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"frameworks/api_analytics_query/internal/database/periscopequerydb"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/google/uuid"
	"strconv"
)

// Only exact private tenant/session/sequence matches establish Mist output evidence.
func (s *PeriscopeServer) enrichNetworkDelivery(ctx context.Context, history *obs.History) error {
	indices := map[string][]int{}
	tenants, sessions, sequences := []string{}, []string{}, []int32{}
	seenTenants, seenSessions, seenSequences := map[string]bool{}, map[string]bool{}, map[int32]bool{}
	key := func(tenant, session string, sequence uint64) string {
		return tenant + "|" + session + "|" + strconv.FormatUint(sequence, 10)
	}
	for i, raw := range history.Details {
		if i >= len(history.Observations) || history.Observations[i].Kind != "segment" {
			continue
		}
		var detail struct {
			Tenant   string `json:"stream_tenant_id"`
			Session  string `json:"delivery_session_id"`
			Sequence uint64 `json:"seq_no"`
		}
		if json.Unmarshal(raw, &detail) != nil || detail.Session == "" || detail.Sequence > 2147483647 {
			continue
		}
		if id, err := uuid.Parse(detail.Tenant); err != nil || id == uuid.Nil {
			continue
		}
		k := key(detail.Tenant, detail.Session, detail.Sequence)
		indices[k] = append(indices[k], i)
		if !seenTenants[detail.Tenant] {
			tenants = append(tenants, detail.Tenant)
			seenTenants[detail.Tenant] = true
		}
		if !seenSessions[detail.Session] {
			sessions = append(sessions, detail.Session)
			seenSessions[detail.Session] = true
		}
		sequence := int32(detail.Sequence)
		if !seenSequences[sequence] {
			sequences = append(sequences, sequence)
			seenSequences[sequence] = true
		}
	}
	if len(indices) == 0 {
		return nil
	}
	rows, err := periscopequerydb.Query(ctx, s.clickhouse, `SELECT toString(tenant_id),livepeer_session_id,segment_number,
 argMax(rendition_count,projection_version_ms),argMax(output_bytes_total,projection_version_ms),argMax(turnaround_ms,projection_version_ms),max(source_ended_at_ms)
 FROM periscope.processing_segments_final WHERE tenant_id IN (?) AND livepeer_session_id IN (?) AND segment_number IN (?) AND process_type='Livepeer'
 GROUP BY tenant_id,livepeer_session_id,segment_number`, tenants, sessions, sequences)
	if err != nil {
		return fmt.Errorf("mist output evidence unavailable: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tenant, session string
		var sequence, renditions int32
		var bytes, turnaround, ended int64
		if err := rows.Scan(&tenant, &session, &sequence, &renditions, &bytes, &turnaround, &ended); err != nil {
			return err
		}
		if renditions <= 0 {
			continue
		}
		for _, i := range indices[key(tenant, session, uint64(sequence))] {
			var detail map[string]any
			if err := json.Unmarshal(history.Details[i], &detail); err != nil {
				return err
			}
			detail["downstream_status"] = "output_observed"
			detail["mist_output"] = map[string]any{"rendition_count": renditions, "output_bytes": bytes, "turnaround_ms": turnaround, "observed_at_ms": ended}
			raw, err := json.Marshal(detail)
			if err != nil {
				return err
			}
			history.Details[i] = raw
		}
	}
	return rows.Err()
}
