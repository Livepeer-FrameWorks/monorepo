package control

import (
	"context"
	"database/sql"
	"encoding/json"

	"frameworks/api_balancing/internal/state"
	pb "frameworks/pkg/proto"
)

// clipRepositoryDB implements state.ClipRepository using the shared DB
type clipRepositoryDB struct{}

func NewClipRepository() state.ClipRepository { return &clipRepositoryDB{} }

func (r *clipRepositoryDB) ListActiveClips(ctx context.Context) ([]state.ClipRecord, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	rows, err := db.QueryContext(ctx, `
		SELECT clip_hash, tenant_id::text, stream_name, COALESCE(node_id::text,''), status, COALESCE(storage_path,''), COALESCE(size_bytes,0)
		FROM foghorn.clips WHERE status != 'deleted'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []state.ClipRecord
	for rows.Next() {
		var rec state.ClipRecord
		if err := rows.Scan(&rec.ClipHash, &rec.TenantID, &rec.InternalName, &rec.NodeID, &rec.Status, &rec.StoragePath, &rec.SizeBytes); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (r *clipRepositoryDB) ResolveInternalNameByRequestID(ctx context.Context, requestID string) (string, error) {
	if db == nil {
		return "", sql.ErrConnDone
	}
	var internalName string
	err := db.QueryRowContext(ctx, `SELECT stream_name FROM foghorn.clips WHERE request_id = $1`, requestID).Scan(&internalName)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return internalName, err
}

func (r *clipRepositoryDB) UpdateClipProgressByRequestID(ctx context.Context, requestID string, percent uint32) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
        UPDATE foghorn.clips 
        SET status = CASE WHEN $2 = 100 THEN 'processing' ELSE 'processing' END,
            updated_at = NOW()
        WHERE request_id = $1`, requestID, percent)
	return err
}

func (r *clipRepositoryDB) UpdateClipDoneByRequestID(ctx context.Context, requestID string, status string, storagePath string, sizeBytes int64, errorMsg string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	var clipStatus string
	if status == "success" {
		clipStatus = string("ready")
	} else {
		clipStatus = string("failed")
	}
	_, err := db.ExecContext(ctx, `
        UPDATE foghorn.clips 
        SET status = $1, 
            storage_path = $2,
            size_bytes = $3,
            error_message = NULLIF($4, ''),
            updated_at = NOW()
        WHERE request_id = $5`, clipStatus, storagePath, sizeBytes, errorMsg, requestID)
	return err
}

// dvrRepositoryDB implements state.DVRRepository using the shared DB
type dvrRepositoryDB struct{}

func NewDVRRepository() state.DVRRepository { return &dvrRepositoryDB{} }

func (r *dvrRepositoryDB) ListAllDVR(ctx context.Context) ([]state.DVRRecord, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	rows, err := db.QueryContext(ctx, `
		SELECT request_hash, tenant_id::text, internal_name, COALESCE(storage_node_id::text,''), COALESCE(storage_node_url,''), status,
		       COALESCE(duration_seconds,0), COALESCE(size_bytes,0), COALESCE(manifest_path,'')
		FROM foghorn.dvr_requests`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []state.DVRRecord
	for rows.Next() {
		var rec state.DVRRecord
		if err := rows.Scan(&rec.Hash, &rec.TenantID, &rec.InternalName, &rec.StorageNodeID, &rec.SourceURL, &rec.Status, &rec.DurationSec, &rec.SizeBytes, &rec.ManifestPath); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (r *dvrRepositoryDB) ResolveInternalNameByHash(ctx context.Context, dvrHash string) (string, error) {
	if db == nil {
		return "", sql.ErrConnDone
	}
	var internalName string
	err := db.QueryRowContext(ctx, `SELECT internal_name FROM foghorn.dvr_requests WHERE request_hash = $1`, dvrHash).Scan(&internalName)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return internalName, err
}

func (r *dvrRepositoryDB) UpdateDVRProgressByHash(ctx context.Context, dvrHash string, status string, sizeBytes int64) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
        UPDATE foghorn.dvr_requests 
        SET status = $2,
            size_bytes = $3,
            updated_at = NOW()
        WHERE request_hash = $1`, dvrHash, status, sizeBytes)
	return err
}

func (r *dvrRepositoryDB) UpdateDVRCompletionByHash(ctx context.Context, dvrHash string, finalStatus string, durationSeconds int64, sizeBytes int64, manifestPath string, errorMsg string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
        UPDATE foghorn.dvr_requests 
        SET status = $1,
            ended_at = NOW(),
            duration_seconds = $2,
            size_bytes = $3,
            manifest_path = $4,
            error_message = NULLIF($5, ''),
            updated_at = NOW()
        WHERE request_hash = $6`, finalStatus, durationSeconds, sizeBytes, manifestPath, errorMsg, dvrHash)
	return err
}

// nodeRepositoryDB implements state.NodeRepository for node outputs/base_url

type nodeRepositoryDB struct{}

func NewNodeRepository() state.NodeRepository { return &nodeRepositoryDB{} }

func (r *nodeRepositoryDB) ListAllNodes(ctx context.Context) ([]state.NodeRecord, error) {
	if db == nil {
		return nil, sql.ErrConnDone
	}
	rows, err := db.QueryContext(ctx, `SELECT node_id, COALESCE(base_url,''), COALESCE(outputs,'{}') FROM foghorn.node_outputs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []state.NodeRecord
	for rows.Next() {
		var rec state.NodeRecord
		var outputsJSON string
		if err := rows.Scan(&rec.NodeID, &rec.BaseURL, &outputsJSON); err != nil {
			return nil, err
		}
		rec.OutputsJSON = outputsJSON
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (r *nodeRepositoryDB) UpsertNodeOutputs(ctx context.Context, nodeID string, baseURL string, outputsJSON string) error {
	if db == nil {
		return sql.ErrConnDone
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO foghorn.node_outputs (node_id, base_url, outputs, last_updated)
		VALUES ($1, NULLIF($2,''), COALESCE($3::jsonb,'{}'::jsonb), NOW())
		ON CONFLICT (node_id) DO UPDATE SET
			base_url = NULLIF(EXCLUDED.base_url,''),
			outputs = COALESCE(EXCLUDED.outputs,'{}'::jsonb),
			last_updated = NOW()
	`, nodeID, baseURL, outputsJSON)
	return err
}

func (r *nodeRepositoryDB) UpsertNodeLifecycle(ctx context.Context, update *pb.NodeLifecycleUpdate) error {
	if db == nil {
		return sql.ErrConnDone
	}
	if update == nil {
		return nil
	}
	// Marshal full lifecycle payload to JSONB for audit and readiness
	b, err := json.Marshal(update)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO foghorn.node_lifecycle (node_id, lifecycle, last_updated)
		VALUES ($1, $2::jsonb, NOW())
		ON CONFLICT (node_id) DO UPDATE SET
			lifecycle = EXCLUDED.lifecycle,
			last_updated = NOW()
	`, update.GetNodeId(), string(b))
	return err
}
