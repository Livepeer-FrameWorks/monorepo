// Package store owns the observer's tenant-scoped PostgreSQL state.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
)

type State struct {
	Resources   map[string]obs.Resource
	Endpoints   map[string]obs.Endpoint
	Observers   map[string]obs.Observer
	Assignments map[string]obs.Assignment
}

func NewState() *State {
	return &State{Resources: map[string]obs.Resource{}, Endpoints: map[string]obs.Endpoint{}, Observers: map[string]obs.Observer{}, Assignments: map[string]obs.Assignment{}}
}

type Store struct {
	DB       *sql.DB
	TenantID string
}

func (s *Store) Snapshot(ctx context.Context) (*State, error) {
	var state *State
	err := database.WithRetryablePostgresTx(ctx, s.DB, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead}, func(tx *sql.Tx) error {
		state = NewState()
		return s.read(ctx, tx, state)
	})
	if err != nil {
		return nil, err
	}
	return state, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Store) read(ctx context.Context, q queryer, state *State) error {
	tables := []struct {
		name string
		put  func(string, []byte) error
	}{
		{"resources", func(id string, raw []byte) error {
			var v obs.Resource
			if e := json.Unmarshal(raw, &v); e != nil {
				return e
			}
			state.Resources[id] = v
			return nil
		}},
		{"endpoints", func(id string, raw []byte) error {
			var v obs.Endpoint
			if e := json.Unmarshal(raw, &v); e != nil {
				return e
			}
			state.Endpoints[id] = v
			return nil
		}},
		{"observers", func(id string, raw []byte) error {
			var v obs.Observer
			if e := json.Unmarshal(raw, &v); e != nil {
				return e
			}
			state.Observers[id] = v
			return nil
		}},
		{"assignments", func(id string, raw []byte) error {
			var v obs.Assignment
			if e := json.Unmarshal(raw, &v); e != nil {
				return e
			}
			state.Assignments[id] = v
			return nil
		}},
	}
	for _, table := range tables {
		rows, err := q.QueryContext(ctx, "SELECT id,document FROM network_observer."+table.name+" WHERE tenant_id=$1 ORDER BY id LIMIT 20001", s.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		count := 0
		for rows.Next() {
			count++
			var id string
			var raw []byte
			if err = rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return err
			}
			if err = table.put(id, raw); err != nil {
				rows.Close()
				return err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if count > 20000 {
			return fmt.Errorf("observer inventory limit exceeded for %s", table.name)
		}
	}
	return nil
}
func documents[V any](values map[string]V) (map[string]string, error) {
	out := map[string]string{}
	for id, v := range values {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("serialize observer document %s: %w", id, err)
		}
		out[id] = string(raw)
	}
	return out, nil
}

func (s *State) documents() (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	var err error
	if out["resources"], err = documents(s.Resources); err != nil {
		return nil, err
	}
	if out["endpoints"], err = documents(s.Endpoints); err != nil {
		return nil, err
	}
	if out["observers"], err = documents(s.Observers); err != nil {
		return nil, err
	}
	if out["assignments"], err = documents(s.Assignments); err != nil {
		return nil, err
	}
	return out, nil
}
func (s *Store) Update(ctx context.Context, apply func(*State, *sql.Tx) error) error {
	return database.WithRetryablePostgresTx(ctx, s.DB, nil, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO network_observer.scheduler_lock (tenant_id) VALUES ($1) ON CONFLICT (tenant_id) DO NOTHING", s.TenantID); err != nil {
			return err
		}
		var tenant string
		if err := tx.QueryRowContext(ctx, "SELECT tenant_id FROM network_observer.scheduler_lock WHERE tenant_id=$1 FOR UPDATE", s.TenantID).Scan(&tenant); err != nil {
			return err
		}
		state := NewState()
		if err := s.read(ctx, tx, state); err != nil {
			return err
		}
		before, err := state.documents()
		if err != nil {
			return err
		}
		if applyErr := apply(state, tx); applyErr != nil {
			return applyErr
		}
		after, err := state.documents()
		if err != nil {
			return err
		}
		for table, values := range after {
			for id, raw := range values {
				if before[table][id] == raw {
					continue
				}
				if _, err := tx.ExecContext(ctx, "INSERT INTO network_observer."+table+" (tenant_id,id,document) VALUES ($1,$2,$3::jsonb) ON CONFLICT (tenant_id,id) DO UPDATE SET document=EXCLUDED.document", s.TenantID, id, raw); err != nil {
					return err
				}
			}
			for id := range before[table] {
				if _, ok := values[id]; !ok {
					if _, err := tx.ExecContext(ctx, "DELETE FROM network_observer."+table+" WHERE tenant_id=$1 AND id=$2", s.TenantID, id); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}
