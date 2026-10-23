// WHEP viewer for the stack scenarios: plays one WHEP endpoint, counts the RTP
// media that arrives, then deletes the session.
//
//	whep <endpoint url> [min packets] [timeout seconds]
//
// It offers receive-only H264 video and Opus audio without trickle ICE (the
// offer carries every gathered candidate), POSTs it as application/sdp,
// follows redirects, and resolves the session URL from the answer's Location
// header. It prints one JSON line:
//
//	{"endpoint":...,"session":...,"tracks":[...],"packets":N,"video_packets":N,"bytes":N,"delete_status":N,"error":...}
//
// and exits 0 only when at least <min packets> video RTP packets arrived and
// the DELETE of the session answered 2xx.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
)

type result struct {
	Endpoint     string   `json:"endpoint"`
	Session      string   `json:"session,omitempty"`
	Tracks       []string `json:"tracks"`
	Packets      int64    `json:"packets"`
	VideoPackets int64    `json:"video_packets"`
	Bytes        int64    `json:"bytes"`
	DeleteStatus int      `json:"delete_status"`
	Error        string   `json:"error,omitempty"`
}

func main() {
	if len(os.Args) < 2 || len(os.Args) > 4 {
		fmt.Fprintln(os.Stderr, "usage: whep <endpoint url> [min packets] [timeout seconds]")
		os.Exit(2)
	}
	minPackets, timeout := int64(50), 30*time.Second
	if len(os.Args) > 2 {
		n, err := strconv.ParseInt(os.Args[2], 10, 64)
		if err != nil || n < 1 {
			fmt.Fprintln(os.Stderr, "whep: min packets must be a positive integer")
			os.Exit(2)
		}
		minPackets = n
	}
	if len(os.Args) > 3 {
		s, err := strconv.Atoi(os.Args[3])
		if err != nil || s < 1 {
			fmt.Fprintln(os.Stderr, "whep: timeout must be a positive number of seconds")
			os.Exit(2)
		}
		timeout = time.Duration(s) * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	res := play(ctx, os.Args[1], minPackets)
	out, err := json.Marshal(res)
	if err != nil {
		fmt.Fprintln(os.Stderr, "whep:", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
	if res.Error != "" || res.VideoPackets < minPackets || res.DeleteStatus < 200 || res.DeleteStatus > 299 {
		os.Exit(1)
	}
}

func play(ctx context.Context, endpoint string, minPackets int64) *result {
	res := &result{Endpoint: endpoint, Tracks: []string{}}
	// Mist's answer declares no a=ssrc lines and negotiates no MID header
	// extension, so incoming streams are matched to their media section by
	// payload type, as browsers do.
	settings := webrtc.SettingEngine{}
	settings.SetHandleUndeclaredSSRCWithoutAnswer(true)
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		res.Error = "peer connection: " + err.Error()
		return res
	}
	defer func() { _ = pc.Close() }()
	for _, kind := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeVideo, webrtc.RTPCodecTypeAudio} {
		if _, addErr := pc.AddTransceiverFromKind(kind, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); addErr != nil {
			res.Error = "transceiver: " + addErr.Error()
			return res
		}
	}

	var packets, videoPackets, received atomic.Int64
	var tracksMu sync.Mutex
	var tracks []string
	enough := make(chan struct{})
	var enoughOnce sync.Once
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		tracksMu.Lock()
		tracks = append(tracks, track.Codec().MimeType)
		tracksMu.Unlock()
		for {
			pkt, _, readErr := track.ReadRTP()
			if readErr != nil {
				return
			}
			received.Add(int64(len(pkt.Payload)))
			packets.Add(1)
			if track.Kind() == webrtc.RTPCodecTypeVideo && videoPackets.Add(1) >= minPackets {
				enoughOnce.Do(func() { close(enough) })
			}
		}
	})

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		res.Error = "offer: " + err.Error()
		return res
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err = pc.SetLocalDescription(offer); err != nil {
		res.Error = "local description: " + err.Error()
		return res
	}
	select {
	case <-gathered:
	case <-ctx.Done():
		res.Error = "ICE gathering did not complete"
		return res
	}

	answer, session, err := postOffer(ctx, endpoint, pc.LocalDescription().SDP)
	res.Session = session
	if err == nil {
		err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer})
	}
	if err != nil {
		res.Error = err.Error()
	} else {
		select {
		case <-enough:
		case <-ctx.Done():
			res.Error = fmt.Sprintf("fewer than %d video RTP packets arrived (ICE state %s)", minPackets, pc.ICEConnectionState())
		}
	}
	res.Packets, res.VideoPackets, res.Bytes = packets.Load(), videoPackets.Load(), received.Load()
	if session != "" {
		res.DeleteStatus = deleteSession(session)
	}
	tracksMu.Lock()
	res.Tracks = append(res.Tracks, tracks...)
	tracksMu.Unlock()
	return res
}

// postOffer sends the offer and returns the answer SDP and the absolute
// session URL. A redirect is followed with the same POST (307/308).
func postOffer(ctx context.Context, endpoint, offer string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte(offer)))
	if err != nil {
		return "", "", fmt.Errorf("offer request: %w", err)
	}
	req.Header.Set("Content-Type", "application/sdp")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("POST offer: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", fmt.Errorf("read answer: %w", err)
	}
	session := ""
	if loc := resp.Header.Get("Location"); loc != "" {
		if ref, err := url.Parse(loc); err == nil {
			session = resp.Request.URL.ResolveReference(ref).String()
		}
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", session, fmt.Errorf("POST offer to %s: HTTP %d: %.200s", resp.Request.URL, resp.StatusCode, body)
	}
	if session == "" {
		return "", "", errors.New("the answer has no Location header for the session")
	}
	return string(body), session, nil
}

// deleteSession ends the session and returns the HTTP status, or 0 when the
// request failed.
func deleteSession(session string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, session, nil)
	if err != nil {
		return 0
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}
