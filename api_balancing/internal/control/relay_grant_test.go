package control

import "testing"

// TestRelayGrantRedisRoundTripAndFailClosed exercises the Redis-backed store
// (the HA path) and the fail-closed behavior when Redis is unreachable.
func TestRelayGrantRedisRoundTripAndFailClosed(t *testing.T) {
	_, client, mr := newTestRedis(t)
	SetRelayGrantRedis(client)
	t.Cleanup(func() { SetRelayGrantRedis(nil) })

	id, err := MintRelayGrant("h1", "node-1", []string{"/internal/artifact/vod/h1.mkv"})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, ok := lookupRelayGrant(id); !ok {
		t.Fatal("grant should be found in redis after mint")
	}

	// Redis unreachable → lookup must fail closed (deny), never allow.
	mr.Close()
	if _, ok := lookupRelayGrant(id); ok {
		t.Fatal("lookup must fail closed when redis is down")
	}
}

func TestRelayGrantMintLookupAndAuthorize(t *testing.T) {
	// In-memory mode (no Redis wired in tests).
	SetRelayGrantRedis(nil)

	const hash = "abc123"
	mediaPath := "/internal/artifact/vod/abc123.mkv"
	dtshPath := mediaPath + ".dtsh"

	id, err := MintRelayGrant(hash, "origin-node-1", []string{mediaPath, dtshPath})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if id == "" {
		t.Fatal("empty grant id")
	}

	g, ok := lookupRelayGrant(id)
	if !ok {
		t.Fatal("grant not found after mint")
	}
	if g.ArtifactHash != hash || g.OriginNodeID != "origin-node-1" {
		t.Fatalf("grant fields = %+v", g)
	}

	const node = "origin-node-1"
	// One grant authorizes both media and its .dtsh sidecar on the bound node.
	if allowed, reason := relayGrantAllows(g, ok, node, hash, mediaPath); !allowed {
		t.Fatalf("media path should be allowed: %s", reason)
	}
	if allowed, reason := relayGrantAllows(g, ok, node, hash, dtshPath); !allowed {
		t.Fatalf(".dtsh path should be allowed: %s", reason)
	}

	// Wrong node, wrong hash, wrong path, and unknown grant all deny.
	if allowed, _ := relayGrantAllows(g, ok, "other-node", hash, mediaPath); allowed {
		t.Fatal("node mismatch should deny")
	}
	if allowed, _ := relayGrantAllows(g, ok, node, "other", mediaPath); allowed {
		t.Fatal("hash mismatch should deny")
	}
	if allowed, _ := relayGrantAllows(g, ok, node, hash, "/internal/artifact/vod/other.mkv"); allowed {
		t.Fatal("path outside grant should deny")
	}
	missing, found := lookupRelayGrant("does-not-exist")
	if allowed, _ := relayGrantAllows(missing, found, node, hash, mediaPath); allowed {
		t.Fatal("unknown grant should deny")
	}
}
