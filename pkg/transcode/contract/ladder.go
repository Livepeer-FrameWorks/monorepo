package contract

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// RenditionSpec is one rendition of a transcode ladder in MistProcAV's own option names: the
// backend-neutral form the tenant's policy, the session triggers and the worker's request all
// carry. A Mist worker runs it as a MistProcAV process with exactly these options.
type RenditionSpec struct {
	Name string `json:"name"`
	// Codec is MistProcAV's output codec.
	Codec string `json:"codec"`
	// Resolution is MistProcAV's resolution: "WxH", or a partial "xH" / "Wx" whose missing side
	// follows the source; empty keeps the source size. On the wire it is resolved to "WxH".
	Resolution string `json:"resolution,omitempty"`
	Bitrate    int64  `json:"bitrate"`
	// GOPSize is MistProcAV's gopsize: milliseconds between keyframes, 0 to follow the source's.
	GOPSize int `json:"gopsize,omitempty"`
	// Profile is MistProcAV's H264 profile (high, main or baseline); empty is the encoder's
	// default. MistProcAV encodes no B-frames, so high is LPMS's constrained high.
	Profile string `json:"profile,omitempty"`
	// TrackInhibit drops the rendition for a source it matches. It is the client's to apply and
	// never crosses the wire.
	TrackInhibit string `json:"track_inhibit,omitempty"`
}

// Requested is the size the resolution asks for; a side it leaves to the source is 0.
func (r RenditionSpec) Requested() (width, height int, err error) {
	if r.Resolution == "" {
		return 0, 0, nil
	}
	w, h, ok := strings.Cut(r.Resolution, "x")
	if !ok {
		return 0, 0, fmt.Errorf("rendition %s resolution %q is not WxH, xH or Wx", r.Name, r.Resolution)
	}
	parse := func(side string) (int, error) {
		if side == "" {
			return 0, nil
		}
		if len(side) > 9 || strings.Trim(side, "0123456789") != "" {
			return 0, errors.New("not a number")
		}
		return strconv.Atoi(side)
	}
	if width, err = parse(w); err == nil {
		height, err = parse(h)
	}
	if err != nil || (width == 0 && height == 0) {
		return 0, 0, fmt.Errorf("rendition %s resolution %q is not WxH, xH or Wx", r.Name, r.Resolution)
	}
	return width, height, nil
}

// Size is the output size MistProcAV encodes for a source of srcWidth x srcHeight, as
// FFmpeg::resolveTargetResolution computes it: a missing side follows the source aspect, "xH"
// names the short side of a portrait source, and odd sides round down to even.
func (r RenditionSpec) Size(srcWidth, srcHeight int) (width, height int, err error) {
	reqWidth, reqHeight, err := r.Requested()
	if err != nil {
		return 0, 0, err
	}
	width, height = srcWidth, srcHeight
	if reqWidth == 0 && reqHeight == 0 {
		return width, height, nil
	}
	if reqWidth == 0 || reqHeight == 0 {
		if srcWidth <= 0 || srcHeight <= 0 {
			return width, height, nil
		}
		switch {
		case reqWidth == 0 && srcWidth < srcHeight:
			reqWidth = reqHeight
			reqHeight = srcHeight * reqWidth / srcWidth
		case reqWidth == 0:
			reqWidth = srcWidth * reqHeight / srcHeight
		default:
			reqHeight = srcHeight * reqWidth / srcWidth
		}
	}
	if reqWidth == 0 || reqHeight == 0 {
		return width, height, nil
	}
	even := func(v int) int {
		if v > 2 && v%2 != 0 {
			return v - 1
		}
		return v
	}
	return even(reqWidth), even(reqHeight), nil
}

// PolicyHeight is the height a class must offer before the source is known: the requested
// height, or 0 when the resolution leaves it to the source.
func (r RenditionSpec) PolicyHeight() int {
	_, height, err := r.Requested()
	if err != nil {
		return 0
	}
	return height
}

// ValidatePolicy checks a policy ladder: named, unique renditions with a codec, a readable
// resolution, a bitrate, a non-negative gopsize and a known profile.
func ValidatePolicy(renditions []RenditionSpec) error {
	if len(renditions) == 0 {
		return errors.New("transcode ladder has no rendition")
	}
	seen := map[string]bool{"source": true}
	for _, r := range renditions {
		if !renditionName.MatchString(r.Name) || seen[r.Name] {
			return fmt.Errorf("rendition name %q is invalid or not unique", r.Name)
		}
		seen[r.Name] = true
		if !encoderName.MatchString(r.Codec) {
			return fmt.Errorf("rendition %s codec %q is invalid", r.Name, r.Codec)
		}
		if _, _, err := r.Requested(); err != nil {
			return err
		}
		if r.Bitrate < 1 || r.GOPSize < 0 || r.GOPSize > maxGOPSizeMs {
			return fmt.Errorf("rendition %s needs a bitrate and a gopsize of 0..%d ms", r.Name, maxGOPSizeMs)
		}
		switch r.Profile {
		case "", "high", "main", "baseline":
		default:
			return fmt.Errorf("rendition %s profile %q is not high, main or baseline", r.Name, r.Profile)
		}
		// MistProcAV hands a rendition's profile to whichever encoder runs it, and these are H264's
		// profiles: an HEVC encoder knows no high or baseline profile, an AV1 encoder none of them.
		if r.Profile != "" && r.Codec != "H264" {
			return fmt.Errorf("rendition %s is %s; a profile applies to H264 only", r.Name, r.Codec)
		}
	}
	return nil
}

// maxGOPSizeMs is the longest keyframe interval a rendition may ask for.
const maxGOPSizeMs = 60000

var renditionName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var encoderName = regexp.MustCompile(`^[A-Z0-9]{1,16}$`)
