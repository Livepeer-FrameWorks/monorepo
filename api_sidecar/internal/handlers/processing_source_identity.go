package handlers

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/google/uuid"
)

// A clip's staged cut carries no Mist track lineage, so the clip records which
// of its video tracks are source tracks when it cuts, from three Mist facts:
//
//  1. Mist's MKV output writes each selected track as TrackNumber = the
//     stream's track index + 1, TrackEntries in ascending index order
//     (mistserver src/output/output_ebml.cpp sendElemTrackEntry/sendHeader).
//  2. The "source" selector picks exactly the stream's original tracks by
//     Mist's own lineage (mistserver lib/stream.cpp, trackLow == "source"), so
//     the same cut URL with video=source names the source TrackNumbers.
//  3. processing+ reads the staged file as a realtime input, which numbers the
//     file's tracks 0..n in file order and does not register a track without
//     frames, or a video track without a keyframe (mistserver
//     src/input/input.cpp realtimeMainLoop, realtimeTrackSkipReason). A cut
//     declares every selected track even when it holds none of its frames: a
//     rendition whose producer was replaced just before the cut. The staged
//     file's blocks are scanned to apply the same rule, so the k-th
//     processing+ video track by id is the k-th registered staged video
//     TrackEntry; counts and heights are checked to prove that.

const (
	ebmlIDSegment     = 0x18538067
	ebmlIDCluster     = 0x1F43B675
	ebmlIDTracks      = 0x1654AE6B
	ebmlIDTrackEntry  = 0xAE
	ebmlIDTrackNumber = 0xD7
	ebmlIDTrackType   = 0x83
	ebmlIDCodecID     = 0x86
	ebmlIDVideo       = 0xE0
	ebmlIDPixelWidth  = 0xB0
	ebmlIDPixelHeight = 0xBA
	ebmlIDBlockGroup  = 0xA0
	ebmlIDBlock       = 0xA1
	ebmlIDSimpleBlock = 0xA3

	mkvTrackTypeVideo = 1

	// The Tracks element precedes the first Cluster; Mist's header is a few
	// KB, so a larger prefix means the input is not a Mist MKV header.
	mkvHeaderReadLimit = 4 << 20
	sourceProbeTimeout = 30 * time.Second
)

type mkvTrack struct {
	number  uint64
	trackTy uint64
	codecID string
	width   int
	height  int
	// realtimeSkip is why Mist's realtime input does not register this track
	// of the staged file; empty when it does.
	realtimeSkip string
}

func (t mkvTrack) isPlayableVideo() bool {
	return t.trackTy == mkvTrackTypeVideo && t.codecID != "V_MJPEG"
}

// mkvBlockPresence is what a file's blocks hold for one TrackNumber.
type mkvBlockPresence struct {
	frames   bool
	keyframe bool
}

// realtimeTrackSkipReason mirrors mistserver src/processing_lifecycle.h for a
// fully indexed file.
func realtimeTrackSkipReason(t mkvTrack, p mkvBlockPresence) string {
	if !p.frames {
		return "it has no frames"
	}
	if t.trackTy == mkvTrackTypeVideo && !p.keyframe {
		return "it has no keyframe"
	}
	return ""
}

// readMKVBlockPresence scans every SimpleBlock and Block of an MKV file and
// reports, per TrackNumber, whether it carries a frame and a keyframe (the
// block flag's top bit, as Mist's EBML input reads it). Only block headers are
// read; payloads are skipped.
func readMKVBlockPresence(r io.Reader) (map[uint64]mkvBlockPresence, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	presence := map[uint64]mkvBlockPresence{}
	for {
		id, size, err := readEBMLElementHeader(br)
		if errors.Is(err, io.EOF) {
			return presence, nil
		}
		if err != nil {
			return nil, fmt.Errorf("mkv blocks: %w", err)
		}
		switch id {
		case ebmlIDSegment, ebmlIDCluster, ebmlIDBlockGroup:
			// Containers: their children follow directly, also when the
			// size is unknown (a live-written Segment or Cluster).
			continue
		case ebmlIDSimpleBlock, ebmlIDBlock:
			if size < 4 {
				return nil, fmt.Errorf("mkv blocks: block of %d bytes", size)
			}
			raw, numLen, err := readEBMLVint(br)
			if err != nil {
				return nil, fmt.Errorf("mkv blocks: block track number: %w", err)
			}
			track := raw & (uint64(1)<<(7*numLen) - 1)
			var head [3]byte // timecode (2) and flags
			if _, err := io.ReadFull(br, head[:]); err != nil {
				return nil, fmt.Errorf("mkv blocks: block header: %w", err)
			}
			payload := size - int64(numLen) - 3
			if payload < 0 {
				return nil, fmt.Errorf("mkv blocks: block of %d bytes", size)
			}
			p := presence[track]
			if payload > 0 {
				p.frames = true
				p.keyframe = p.keyframe || head[2]&0x80 != 0
			}
			presence[track] = p
			if _, err := br.Discard(int(payload)); err != nil {
				return nil, fmt.Errorf("mkv blocks: skip block: %w", err)
			}
		default:
			if size < 0 {
				return nil, fmt.Errorf("mkv blocks: element %#x of unknown size", id)
			}
			if _, err := br.Discard(int(size)); err != nil {
				return nil, fmt.Errorf("mkv blocks: skip element %#x: %w", id, err)
			}
		}
	}
}

// readMKVTracks reads an MKV stream up to and including its first Tracks
// element and returns the TrackEntries in file order.
func readMKVTracks(r io.Reader) ([]mkvTrack, error) {
	br := bufio.NewReader(io.LimitReader(r, mkvHeaderReadLimit))
	for {
		id, size, err := readEBMLElementHeader(br)
		if err != nil {
			return nil, fmt.Errorf("mkv header: %w", err)
		}
		switch id {
		case ebmlIDSegment:
			// Descend: the Segment's children follow directly.
			continue
		case ebmlIDTracks:
			if size < 0 || size > mkvHeaderReadLimit {
				return nil, fmt.Errorf("mkv header: Tracks element size %d unsupported", size)
			}
			body := make([]byte, size)
			if _, err := io.ReadFull(br, body); err != nil {
				return nil, fmt.Errorf("mkv header: read Tracks: %w", err)
			}
			return parseMKVTrackEntries(body)
		case ebmlIDCluster:
			return nil, errors.New("mkv header: media cluster before Tracks")
		default:
			if size < 0 || size > mkvHeaderReadLimit {
				return nil, fmt.Errorf("mkv header: element %#x size %d before Tracks unsupported", id, size)
			}
			if _, err := br.Discard(int(size)); err != nil {
				return nil, fmt.Errorf("mkv header: skip element %#x: %w", id, err)
			}
		}
	}
}

func parseMKVTrackEntries(body []byte) ([]mkvTrack, error) {
	var tracks []mkvTrack
	err := walkEBMLChildren(body, func(id uint64, data []byte) error {
		if id != ebmlIDTrackEntry {
			return nil
		}
		var t mkvTrack
		if err := walkEBMLChildren(data, func(id uint64, data []byte) error {
			switch id {
			case ebmlIDTrackNumber:
				t.number = ebmlUint(data)
			case ebmlIDTrackType:
				t.trackTy = ebmlUint(data)
			case ebmlIDCodecID:
				t.codecID = strings.TrimRight(string(data), "\x00")
			case ebmlIDVideo:
				return walkEBMLChildren(data, func(id uint64, data []byte) error {
					switch id {
					case ebmlIDPixelWidth:
						t.width = int(ebmlUint(data))
					case ebmlIDPixelHeight:
						t.height = int(ebmlUint(data))
					}
					return nil
				})
			}
			return nil
		}); err != nil {
			return err
		}
		if t.number == 0 {
			return errors.New("mkv header: TrackEntry without TrackNumber")
		}
		tracks = append(tracks, t)
		return nil
	})
	return tracks, err
}

func walkEBMLChildren(body []byte, fn func(id uint64, data []byte) error) error {
	r := &sliceByteReader{b: body}
	for r.pos < len(body) {
		id, size, err := readEBMLElementHeader(r)
		if err != nil {
			return err
		}
		if size < 0 || r.pos+int(size) > len(body) {
			return fmt.Errorf("mkv header: element %#x overruns its parent", id)
		}
		if err := fn(id, body[r.pos:r.pos+int(size)]); err != nil {
			return err
		}
		r.pos += int(size)
	}
	return nil
}

type sliceByteReader struct {
	b   []byte
	pos int
}

func (r *sliceByteReader) ReadByte() (byte, error) {
	if r.pos >= len(r.b) {
		return 0, io.ErrUnexpectedEOF
	}
	c := r.b[r.pos]
	r.pos++
	return c, nil
}

// readEBMLElementHeader reads an element ID (marker bits kept, as IDs are
// written) and its data size; size is -1 for the reserved unknown size.
func readEBMLElementHeader(r io.ByteReader) (uint64, int64, error) {
	id, idLen, err := readEBMLVint(r)
	if err != nil {
		return 0, 0, err
	}
	if idLen > 4 {
		return 0, 0, fmt.Errorf("invalid EBML element ID length %d", idLen)
	}
	raw, sizeLen, err := readEBMLVint(r)
	if err != nil {
		return 0, 0, err
	}
	mask := uint64(1)<<(7*sizeLen) - 1
	data := raw & mask
	if data == mask {
		return id, -1, nil
	}
	if data > 1<<62 {
		return 0, 0, fmt.Errorf("EBML element %#x size %d out of range", id, data)
	}
	return id, int64(data), nil
}

// readEBMLVint returns the raw big-endian value including its length marker
// and the encoded length in bytes.
func readEBMLVint(r io.ByteReader) (uint64, uint, error) {
	first, err := r.ReadByte()
	if err != nil {
		return 0, 0, err
	}
	length := uint(1)
	for mask := byte(0x80); mask != 0 && first&mask == 0; mask >>= 1 {
		length++
	}
	if length > 8 {
		return 0, 0, errors.New("invalid EBML variable-length integer")
	}
	v := uint64(first)
	for i := uint(1); i < length; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, 0, err
		}
		v = v<<8 | uint64(b)
	}
	return v, length, nil
}

func ebmlUint(data []byte) uint64 {
	var v uint64
	for _, b := range data {
		v = v<<8 | uint64(b)
	}
	return v
}

// clipSourceProbeURL is the cut URL narrowed to the stream's original tracks.
// The probe is read only up to the MKV Tracks header.
func clipSourceProbeURL(cutURL string) (string, error) {
	u, err := url.Parse(cutURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("video", "source")
	q.Set("audio", "source")
	q.Set("subtitle", "none")
	q.Set("meta", "none")
	q.Set("tkn", mist.ProcessingReadSessionPrefix+uuid.NewString())
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// probeClipSourceTrackNumbers asks the Mist serving the cut which TrackNumbers
// its source video tracks carry in an MKV cut of the stream.
func probeClipSourceTrackNumbers(ctx context.Context, cutURL string) (map[uint64]bool, error) {
	probeURL, err := clipSourceProbeURL(cutURL)
	if err != nil {
		return nil, fmt.Errorf("source probe URL: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, sourceProbeTimeout)
	defer cancel()
	resp, err := httpGetSource(ctx, probeURL)
	if err != nil {
		return nil, fmt.Errorf("source probe: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("source probe HTTP status %d", resp.StatusCode)
	}
	tracks, err := readMKVTracks(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("source probe: %w", err)
	}
	numbers := map[uint64]bool{}
	for _, t := range tracks {
		if t.isPlayableVideo() {
			numbers[t.number] = true
		}
	}
	return numbers, nil
}

// readStagedMKVTracks returns the staged file's TrackEntries in file order,
// each marked with whether Mist's realtime input registers it.
func readStagedMKVTracks(path string) ([]mkvTrack, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tracks, err := readMKVTracks(f)
	if err != nil {
		return nil, err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	presence, err := readMKVBlockPresence(f)
	if err != nil {
		return nil, err
	}
	for i := range tracks {
		tracks[i].realtimeSkip = realtimeTrackSkipReason(tracks[i], presence[tracks[i].number])
	}
	return tracks, nil
}

// clipSourceIdentity is what a clip records at cut time: the staged file's
// video TrackEntries that processing+ registers, in file order, and which
// TrackNumbers are source tracks.
type clipSourceIdentity struct {
	stagedVideo   []mkvTrack
	sourceNumbers map[uint64]bool
}

func newClipSourceIdentity(staged []mkvTrack, sourceNumbers map[uint64]bool) *clipSourceIdentity {
	id := &clipSourceIdentity{sourceNumbers: sourceNumbers}
	for _, t := range staged {
		if t.isPlayableVideo() && t.realtimeSkip == "" {
			id.stagedVideo = append(id.stagedVideo, t)
		}
	}
	return id
}

// sourceTrackIDs maps the identity onto the processing+ video tracks: it
// returns the processing+ track ids of the source tracks.
func (c *clipSourceIdentity) sourceTrackIDs(videoTracks []processingMetaVideoTrack) (map[int64]bool, error) {
	staged := make([]processingMetaVideoTrack, 0, len(videoTracks))
	for _, t := range videoTracks {
		// Process outputs on the processing+ stream (thumbnails) carry Mist
		// lineage and were never part of the staged file.
		if t.source != "" {
			continue
		}
		if !t.hasTrackID {
			return nil, fmt.Errorf("processing track %q has no track id", t.name)
		}
		staged = append(staged, t)
	}
	sort.Slice(staged, func(i, j int) bool { return staged[i].trackID < staged[j].trackID })
	if len(staged) != len(c.stagedVideo) {
		return nil, fmt.Errorf("processing stream has %d staged video tracks, staged file declares %d", len(staged), len(c.stagedVideo))
	}
	ids := map[int64]bool{}
	for k, t := range staged {
		entry := c.stagedVideo[k]
		if t.height != entry.height {
			return nil, fmt.Errorf("processing track id %d is %dp, staged track %d is %dp", t.trackID, t.height, entry.number, entry.height)
		}
		if c.sourceNumbers[entry.number] {
			ids[t.trackID] = true
		}
	}
	return ids, nil
}
