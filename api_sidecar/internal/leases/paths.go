package leases

import (
	"path/filepath"
)

// DeterministicPathsForAsset returns the canonical filesystem paths the
// read-through relay may write to for an asset. Includes the in-flight
// `.partial` tmpfile a background fill uses before atomic-renaming into
// place, plus the `.dtsh`/`.gop` sidecars Mist may PUT, so cleanup can't
// race the writer.
//
// Paths are *candidates* — most assets won't have all of them on disk.
// Cleanup only checks IsPathLeased, so listing a path that never
// materializes is harmless. mediaExt is the playback-format extension
// (".mkv", ".mp4", etc.).
//
// streamInternal is the Mist routing name owning the asset; for clips and
// DVR the on-disk layout nests under it (clips/<stream>/<hash>.<ext>,
// dvr/<stream>/<dvr_hash>/...). Pass empty to fall back to the flat
// layout for callers that don't have the stream name yet — the flat
// candidate is always listed so warm-only artifacts stay protected.
//
// For DVR, segmentNames lists the specific segments under
// dvr/<stream>/<dvr_hash>/segments/. Pass an empty slice if the caller
// does not yet have the segment list.
func DeterministicPathsForAsset(basePath string, key AssetKey, mediaExt, streamInternal string, segmentNames []string) []string {
	if basePath == "" || key.Hash == "" {
		return nil
	}
	var out []string
	switch key.Type {
	case "vod":
		// VOD layout is flat: storage/vod/<hash>.<ext>.
		if mediaExt == "" {
			return nil
		}
		flat := filepath.Join(basePath, "vod", key.Hash+mediaExt)
		out = append(out,
			flat,
			flat+".partial",
			flat+".dtsh",
			flat+".gop",
			flat+".blocks", // relay block cache dir; protected as a single path
		)
	case "clip":
		// Clip layout is stream-nested:
		// storage/clips/<stream_internal_name>/<hash>.<ext>. The clip's
		// source stream name is required to build the nested path, so callers
		// must supply streamInternal — without it there's no deterministic
		// on-disk path to protect.
		if mediaExt == "" || streamInternal == "" {
			return nil
		}
		nested := filepath.Join(basePath, "clips", streamInternal, key.Hash+mediaExt)
		out = append(out,
			nested,
			nested+".partial",
			nested+".dtsh",
			nested+".gop",
			nested+".blocks",
		)
	case "upload":
		dir := filepath.Join(basePath, "upload")
		if mediaExt == "" {
			return nil
		}
		base := filepath.Join(dir, key.Hash+mediaExt)
		out = append(out,
			base,
			base+".partial",
			base+".blocks",
		)
	case "dvr":
		// Freeze writes to storage/dvr/<stream>/<dvr_hash>/...
		// (../api_balancing/internal/control/server.go:5013 sets
		// localRoot = filepath.Join(storageBase, "dvr", streamName,
		// chapter.ArtifactHash)). When streamInternal is empty the
		// flat layout is added as a fallback so older deployments and
		// boot-time leases (no stream name resolved yet) still pin
		// something protective.
		roots := []string{filepath.Join(basePath, "dvr", key.Hash)}
		if streamInternal != "" {
			roots = append(roots, filepath.Join(basePath, "dvr", streamInternal, key.Hash))
		}
		for _, dvrDir := range roots {
			// Only segment files matter for DVR leases. Chapter playback
			// is addressed as the chapter's own VOD artifact and pins
			// itself via the vod/ lease path.
			segDir := filepath.Join(dvrDir, "segments")
			for _, name := range segmentNames {
				if name == "" {
					continue
				}
				out = append(out, filepath.Join(segDir, name))
			}
		}
	}
	return out
}
