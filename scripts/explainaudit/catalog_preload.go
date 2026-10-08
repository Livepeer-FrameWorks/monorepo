package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

type catalogSample struct {
	ConnectMS             float64 `json:"connect_ms"`
	ColdMS                float64 `json:"cold_query_ms"`
	WarmMS                float64 `json:"warm_query_ms"`
	ColdCatalogReads      float64 `json:"cold_catalog_reads"`
	WarmCatalogReads      float64 `json:"warm_catalog_reads"`
	BackendAllocatedBytes int64   `json:"backend_allocated_bytes"`
}

// This measures catalog planning with an absent synthetic tenant on the real Purser
// baseline. It is not a workload-throughput or cross-region latency benchmark.
func probeCatalogPreload(opts options) error {
	engine, err := engineFromEnv()
	if err != nil {
		return err
	}
	catalog, _, _, err := buildCatalog(opts.repo)
	if err != nil {
		return err
	}
	statement := ""
	for _, query := range catalog["purser"] {
		if query.Name == "GetTenantAdmissionStatus" {
			statement = query.SQL
		}
	}
	if statement == "" {
		return fmt.Errorf("purser admission query not found")
	}
	colocated, err := engine.declaredColocated("purser")
	if err != nil {
		return err
	}
	const database = "catalog_preload_probe"
	setup, err := engine.createDatabase("purser", database, colocated)
	if err != nil {
		return err
	}
	if err = setup.Close(); err != nil {
		return err
	}
	defer func() {
		if cleanupErr := engine.dropDatabase(database); cleanupErr != nil {
			log.Printf("cleanup catalog probe: %v", cleanupErr)
		}
	}()
	dsn, err := url.Parse(engine.adminDSN)
	if err != nil {
		return err
	}
	dsn.Path = "/" + database
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var samples []catalogSample
	for i := 0; i < 10; i++ {
		sample, sampleErr := catalogConnectionSample(ctx, dsn.String(), statement)
		if sampleErr != nil {
			return sampleErr
		}
		samples = append(samples, sample)
	}
	output := struct {
		Preload     string          `json:"preload"`
		Description string          `json:"description"`
		Samples     []catalogSample `json:"samples"`
	}{os.Getenv("FRAMEWORKS_YUGABYTE_CATALOG_PRELOAD"), "Isolated local engine, Purser baseline, absent tenant; allocated backend memory excludes shared tserver/master memory. Compare connect+cold as well as warm latency.", samples}
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(opts.out, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(opts.out, "catalog-preload-"+output.Preload+".json"), append(data, '\n'), 0644)
}

func catalogConnectionSample(ctx context.Context, dsn, statement string) (catalogSample, error) {
	var sample catalogSample
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return sample, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	started := time.Now()
	conn, err := db.Conn(ctx)
	if err != nil {
		return sample, err
	}
	defer conn.Close()
	sample.ConnectMS = float64(time.Since(started).Microseconds()) / 1000
	for n := 0; n < 2; n++ {
		var raw []byte
		started = time.Now()
		err = conn.QueryRowContext(ctx, "EXPLAIN (ANALYZE, DIST, FORMAT JSON) "+statement, "EUR", "00000000-0000-0000-0000-000000000001").Scan(&raw)
		if err != nil {
			return sample, err
		}
		elapsed := float64(time.Since(started).Microseconds()) / 1000
		var plans []planRoot
		if err = json.Unmarshal(raw, &plans); err != nil {
			return sample, err
		}
		if len(plans) != 1 {
			return sample, fmt.Errorf("expected one admission plan")
		}
		if n == 0 {
			sample.ColdMS, sample.ColdCatalogReads = elapsed, plans[0].CatalogReads
		} else {
			sample.WarmMS, sample.WarmCatalogReads = elapsed, plans[0].CatalogReads
		}
	}
	err = conn.QueryRowContext(ctx, "SELECT sum(total_bytes) FROM pg_backend_memory_contexts").Scan(&sample.BackendAllocatedBytes)
	return sample, err
}
