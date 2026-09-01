package control

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

func TestTranscodeJobTokenRoundTripAndTamper(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	claims := TranscodeJobClaims{
		ManifestID: "processing+artifact", JobID: "job-1", AttemptOrGeneration: "2", Session: "job-1",
		NodeID: "edge-1", ClusterID: "media-eu", TenantID: "tenant-1", SpecDigest: "abc",
		AllowedGatewayClusterIDs: []string{"media-us", "media-eu", "media-eu"}, IssuedAt: now.Unix(),
	}
	token, err := MintTranscodeJobToken("secret", claims)
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyTranscodeJobToken("secret", token, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.JobID != "job-1" || got.NodeID != "edge-1" || len(got.AllowedGatewayClusterIDs) != 2 {
		t.Fatalf("unexpected claims: %+v", got)
	}
	if !TranscodeJobTokenAllowsGatewayCluster(got, "media-us") {
		t.Fatal("expected media-us to be allowed")
	}
	if _, err := VerifyTranscodeJobToken("other", token, now); err == nil {
		t.Fatal("wrong secret accepted")
	}
	tampered := token[:len(token)-1] + "A"
	if _, err := VerifyTranscodeJobToken("secret", tampered, now); err == nil {
		t.Fatal("tampered token accepted")
	}
}

func TestTranscodeJobTokenRequiresCompleteBinding(t *testing.T) {
	if _, err := MintTranscodeJobToken("secret", TranscodeJobClaims{}); err == nil {
		t.Fatal("incomplete claims accepted")
	}
}

func TestTranscodeJobTokenRejectsFutureIssuedAtUnknownFieldsAndTrailingJSON(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	claims := TranscodeJobClaims{
		ManifestID: "live+stream", AttemptOrGeneration: "state:1", Session: "state:1",
		NodeID: "edge-1", ClusterID: "media-eu", TenantID: "tenant-1", SpecDigest: "abc",
		AllowedGatewayClusterIDs: []string{"gateway-eu"}, IssuedAt: now.Add(6 * time.Minute).Unix(),
	}
	token, err := MintTranscodeJobToken("secret", claims)
	if err != nil {
		t.Fatal(err)
	}
	if _, verifyErr := VerifyTranscodeJobToken("secret", token, now); verifyErr == nil {
		t.Fatal("future-issued token accepted")
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	var withUnknown map[string]any
	if err := json.Unmarshal(payload, &withUnknown); err != nil {
		t.Fatal(err)
	}
	withUnknown["attacker_controlled"] = true
	unknownPayload, _ := json.Marshal(withUnknown)
	if _, err := VerifyTranscodeJobToken("secret", signedTranscodePayload("secret", unknownPayload), now.Add(6*time.Minute)); err == nil {
		t.Fatal("token with unknown claim accepted")
	}
	trailing := append(append([]byte{}, payload...), []byte(` {}`)...)
	if _, err := VerifyTranscodeJobToken("secret", signedTranscodePayload("secret", trailing), now.Add(6*time.Minute)); err == nil {
		t.Fatal("token with trailing JSON accepted")
	}
}

func signedTranscodePayload(secret string, payload []byte) string {
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(transcodeJobTokenDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(encoded))
	return "v1." + encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestStampTranscodeJobConfigBindsAuthoritativeSpec(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	authoritative := `[{"process":"Livepeer","workload":"vod","deadline_ms":30000,"min_speed":0.5,"frameworks_gateway_cluster_ids":["gateway-b","gateway-a"],"target_profiles":[{"name":"360p","height":360,"bitrate":900000}]}]`
	stamped, err := StampTranscodeJobConfig(authoritative, "secret", TranscodeJobClaims{
		ManifestID: "processing+artifact", JobID: "job-1", AttemptOrGeneration: "2", Session: "job-1",
		NodeID: "edge-1", ClusterID: "media-eu", TenantID: "tenant-1",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	var processes []map[string]any
	if unmarshalErr := json.Unmarshal([]byte(stamped), &processes); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	token, _ := processes[0]["job_token"].(string)
	claims, err := VerifyTranscodeJobToken("secret", token, now)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := mist.LivepeerJobSpecDigest(authoritative)
	if err != nil {
		t.Fatal(err)
	}
	if claims.SpecDigest != wantDigest || claims.ManifestID != "processing+artifact" || claims.NodeID != "edge-1" {
		t.Fatalf("unexpected stamped claims: %+v", claims)
	}
	if token == "" || mist.StripLivepeerJobToken(stamped) == stamped {
		t.Fatal("delivery config did not contain a removable job token")
	}
}
