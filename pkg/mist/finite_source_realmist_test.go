//go:build media_verify

package mist

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

// Uses a new controller and synthetic media, independently of platform policy,
// database recovery and clock state retained by an existing development stack.
func TestFiniteLiveSource_RealMist(t *testing.T) {
	image := os.Getenv("MIST_CONTRACT_IMAGE")
	if image == "" {
		t.Fatal("MIST_CONTRACT_IMAGE must identify the exact Mist/ffmpeg image under test")
	}
	fixture, err := filepath.Abs("testdata/finite-source.json")
	if err != nil {
		t.Fatal(err)
	}
	hook, err := filepath.Abs("testdata/push-connector-hook.sh")
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("fw-finite-source-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if t.Failed() && os.Getenv("MIST_CONTRACT_KEEP_FAILED") == "1" {
			t.Logf("preserved failed fixture: %s", name)
			return
		}
		if output, err := dockerpg.CLI("rm", "-f", name); err != nil {
			t.Errorf("remove owned fixture: %v %s", err, output)
		}
	})
	command := "ffmpeg -hide_banner -loglevel error -f lavfi -i testsrc2=size=160x90:rate=10 -f lavfi -i sine=frequency=440:sample_rate=48000 -t 12 -c:v libx264 -g 10 -pix_fmt yuv420p -c:a aac /tmp/source.mp4 && exec MistController -c /tmp/finite-source.json"
	if output, err := dockerpg.Run("run", "-d", "--name", name, "--shm-size=256m", "-p", "127.0.0.1::18080", "-v", fixture+":/tmp/finite-source.json:ro", "-v", hook+":/tmp/push-connector-hook.sh:ro", "--entrypoint", "/bin/sh", image, "-c", command); err != nil {
		t.Fatalf("start fixture: %v %s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "18080/tcp")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://127.0.0.1:" + port
	client := &http.Client{Timeout: 20 * time.Second}
	deadline := time.Now().Add(45 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if _, err := dockerpg.CLI("exec", name, "sh", "-c", "test -s /tmp/source.mp4 && pgrep MistController >/dev/null"); err == nil {
			ready = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !ready {
		t.Fatal("controller did not start")
	}
	publish := "for attempt in 1 2 3 4 5; do ffmpeg -hide_banner -loglevel error -re -stream_loop -1 -i /tmp/source.mp4 -t 120 -c copy -flvflags no_metadata -f flv rtmp://127.0.0.1:1935/live/pushed && exit; sleep 1; done"
	if output, err := dockerpg.CLI("exec", "-d", name, "sh", "-c", publish); err != nil {
		t.Fatalf("publisher: %v %s", err, output)
	}
	time.Sleep(15 * time.Second)
	for _, tc := range []struct{ name, query string }{
		{"media-time", "start=2000&stop=7000"},
		{"wall-time", "startunix=-8&duration=5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/pushed.mkv?audio=all&video=all&rate=0&"+tc.query, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
			if resp.StatusCode != http.StatusOK || readErr != nil || len(data) > 8<<20 || !bytes.HasPrefix(data, []byte{0x1a, 0x45, 0xdf, 0xa3}) {
				logs, _ := dockerpg.CLI("logs", "--tail", "50", name)
				t.Fatalf("finite source: status=%d bytes=%d error=%v\n%s", resp.StatusCode, len(data), readErr, logs)
			}
			path := filepath.Join(t.TempDir(), "source.mkv")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if output, err := dockerpg.CLI("cp", path, name+":/tmp/extracted.mkv"); err != nil {
				t.Fatalf("stage probe: %v %s", err, output)
			}
			output, err := dockerpg.CLI("exec", name, "sh", "-c", "ffprobe -v error -count_frames -show_packets -show_entries packet=pts_time,duration_time:stream=codec_name,nb_read_frames -of json /tmp/extracted.mkv 2>/tmp/finite-source-probe-errors")
			if err != nil {
				t.Fatalf("probe extracted media: %v %s", err, output)
			}
			probeErrors, err := dockerpg.CLI("exec", name, "cat", "/tmp/finite-source-probe-errors")
			if err != nil || strings.TrimSpace(probeErrors) != "" {
				t.Fatalf("extracted media has parser/decode errors: %v\n%s\n%s", err, probeErrors, output)
			}
			var probe struct {
				Streams []struct {
					CodecName string `json:"codec_name"`
					Frames    string `json:"nb_read_frames"`
				} `json:"streams"`
				Packets []struct {
					PTS      string `json:"pts_time"`
					Duration string `json:"duration_time"`
				} `json:"packets"`
			}
			if err := json.Unmarshal([]byte(output), &probe); err != nil {
				t.Fatalf("invalid probe response: %v\n%s", err, output)
			}
			// A finite stream need not advertise Duration in its EBML header.
			// Measure actual packet coverage, not a predicted container duration.
			first, last := math.Inf(1), math.Inf(-1)
			for _, packet := range probe.Packets {
				pts, err := strconv.ParseFloat(packet.PTS, 64)
				if err != nil {
					t.Fatalf("packet has no usable timestamp: %q", packet.PTS)
				}
				packetDuration, _ := strconv.ParseFloat(packet.Duration, 64)
				first = math.Min(first, pts)
				last = math.Max(last, pts+packetDuration)
			}
			duration := last - first
			if len(probe.Packets) == 0 || duration < 4 || duration > 7 || !strings.Contains(output, "h264") || !strings.Contains(output, "aac") {
				t.Fatalf("wrong extracted range/tracks: %s", output)
			}
			for _, stream := range probe.Streams {
				if stream.CodecName == "h264" {
					frames, err := strconv.Atoi(stream.Frames)
					if err != nil || frames < 40 || frames > 70 {
						t.Fatalf("video does not cover requested range: %s", output)
					}
				}
			}
			t.Logf("finite source EOF: %d bytes, %.3f seconds, H264 + AAC", len(data), duration)
		})
	}
	for _, format := range []string{"mkv", "mp4"} {
		t.Run("vod-byte-range-"+format, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/vod."+format, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
			resp.Body.Close()
			if os.Getenv("MIST_CONTRACT_KEEP_FAILED") == "1" {
				path := filepath.Join(t.TempDir(), "vod-response.mkv")
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				if output, err := dockerpg.CLI("cp", path, name+":/tmp/vod-response.mkv"); err != nil {
					t.Fatalf("stage VOD response: %v %s", err, output)
				}
			}
			if resp.StatusCode != http.StatusOK || readErr != nil || len(data) < 2000 || len(data) > 8<<20 || (resp.ContentLength >= 0 && resp.ContentLength != int64(len(data))) || (format == "mkv" && resp.ContentLength < 0) {
				t.Fatalf("VOD response: status=%d length=%d bytes=%d error=%v", resp.StatusCode, resp.ContentLength, len(data), readErr)
			}
			path := filepath.Join(t.TempDir(), "full-vod."+format)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if output, err := dockerpg.CLI("cp", path, name+":/tmp/full-vod."+format); err != nil {
				t.Fatalf("stage full VOD: %v %s", err, output)
			}
			output, err := dockerpg.CLI("exec", name, "ffprobe", "-v", "error", "-count_packets", "-show_entries", "stream=codec_name,nb_read_packets", "-of", "json", "/tmp/full-vod."+format)
			var probe struct {
				Streams []struct {
					Codec   string `json:"codec_name"`
					Packets string `json:"nb_read_packets"`
				} `json:"streams"`
			}
			if err != nil || json.Unmarshal([]byte(output), &probe) != nil || len(probe.Streams) != 2 {
				t.Fatalf("full VOD packet probe: %v %s", err, output)
			}
			for _, stream := range probe.Streams {
				want := map[string]string{"h264": "120", "aac": "564"}[stream.Codec]
				if want == "" || stream.Packets != want {
					t.Fatalf("full VOD dropped or added packets: %s", output)
				}
			}
			for _, header := range []string{"bytes=-0", "bytes=2-1", "bytes=18446744073709551616-", fmt.Sprintf("bytes=%d-", len(data))} {
				req.Header.Set("Range", header)
				invalid, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				io.Copy(io.Discard, io.LimitReader(invalid.Body, 4096))
				invalid.Body.Close()
				if invalid.StatusCode != http.StatusRequestedRangeNotSatisfiable || invalid.Header.Get("Content-Range") != fmt.Sprintf("bytes */%d", len(data)) {
					t.Fatalf("invalid range %s: status=%d content-range=%s", header, invalid.StatusCode, invalid.Header.Get("Content-Range"))
				}
			}
			req.Header.Set("Range", "bytes=1000-1999")
			resp, err = client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			rangeData, readErr := io.ReadAll(io.LimitReader(resp.Body, 2001))
			resp.Body.Close()
			if resp.StatusCode != http.StatusPartialContent || readErr != nil || !bytes.Equal(rangeData, data[1000:2000]) {
				t.Fatalf("VOD byte range changed: status=%d bytes=%d error=%v", resp.StatusCode, len(rangeData), readErr)
			}
			for _, interval := range []struct {
				header     string
				start, end int
			}{
				{"bytes=0-0", 0, 0},
				{"bytes=0-99", 0, 99},
				{"bytes=1000-1999", 1000, 1999},
				{fmt.Sprintf("bytes=%d-%d", len(data)-100, len(data)-1), len(data) - 100, len(data) - 1},
				{"bytes=-1", len(data) - 1, len(data) - 1},
				{"bytes=-100", len(data) - 100, len(data) - 1},
				{fmt.Sprintf("bytes=%d-", len(data)-100), len(data) - 100, len(data) - 1},
			} {
				conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 5*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				conn.SetDeadline(time.Now().Add(20 * time.Second))
				req.Close = true
				req.Header.Set("Range", interval.header)
				reader := bufio.NewReader(conn)
				head := req.Clone(t.Context())
				head.Method, head.Close = http.MethodHead, false
				if err := head.Write(conn); err != nil {
					conn.Close()
					t.Fatal(err)
				}
				headResponse, err := http.ReadResponse(reader, head)
				if err != nil {
					conn.Close()
					t.Fatal(err)
				}
				headResponse.Body.Close()
				if headResponse.StatusCode != http.StatusPartialContent || headResponse.ContentLength != int64(interval.end-interval.start+1) {
					conn.Close()
					t.Fatalf("HEAD range %s: status=%d length=%d", interval.header, headResponse.StatusCode, headResponse.ContentLength)
				}
				// Reuse the connection: any illegal HEAD body corrupts this response.
				if err := req.Write(conn); err != nil {
					conn.Close()
					t.Fatal(err)
				}
				response, err := http.ReadResponse(reader, req)
				if err != nil {
					conn.Close()
					t.Fatal(err)
				}
				body, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20))
				response.Body.Close()
				_, tailErr := reader.ReadByte()
				conn.Close()
				contentRange := fmt.Sprintf("bytes %d-%d/%d", interval.start, interval.end, len(data))
				if response.StatusCode != http.StatusPartialContent || response.Header.Get("Content-Range") != contentRange || readErr != nil || !bytes.Equal(body, data[interval.start:interval.end+1]) || tailErr != io.EOF {
					t.Fatalf("range %s: status=%d content-range=%s body=%d read=%v trailing read=%v (must be EOF)", interval.header, response.StatusCode, response.Header.Get("Content-Range"), len(body), readErr, tailErr)
				}
			}
		})
	}
}
