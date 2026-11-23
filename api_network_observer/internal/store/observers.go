package store

import (
	"context"
	"encoding/json"

	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
)

func (s *Store) Observers(ctx context.Context) ([]obs.Observer, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT document FROM network_observer.observers WHERE tenant_id=$1 ORDER BY id", s.TenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []obs.Observer
	for rows.Next() {
		var raw []byte
		var observer obs.Observer
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &observer); err != nil {
			return nil, err
		}
		result = append(result, observer)
	}
	return result, rows.Err()
}
