package contract

import "testing"

func TestRenditionPolicyRefusesAmbiguousOrUnsafeNames(t *testing.T) {
	valid := RenditionSpec{Name: "720p", Codec: "H264", Resolution: "x720", Bitrate: 4500000, Profile: "high"}
	if err := ValidatePolicy([]RenditionSpec{valid}); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*RenditionSpec){
		"reserved source": func(r *RenditionSpec) { r.Name = "source" }, "path": func(r *RenditionSpec) { r.Name = "../720p" },
		"no bitrate": func(r *RenditionSpec) { r.Bitrate = 0 }, "unreadable size": func(r *RenditionSpec) { r.Resolution = "720p" },
		"invalid codec": func(r *RenditionSpec) { r.Codec = "libx264;command" }, "invalid profile": func(r *RenditionSpec) { r.Profile = "extreme" },
		"HEVC profile": func(r *RenditionSpec) { r.Codec = "HEVC" }, "AV1 profile": func(r *RenditionSpec) { r.Codec, r.Profile = "AV1", "main" },
	} {
		t.Run(name, func(t *testing.T) {
			r := valid
			change(&r)
			if ValidatePolicy([]RenditionSpec{r}) == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
	if ValidatePolicy([]RenditionSpec{valid, valid}) == nil {
		t.Fatal("duplicate rendition accepted")
	}
}
func TestRenditionSizeKeepsAspectAndEvenDimensions(t *testing.T) {
	for _, test := range []struct {
		sourceW, sourceH int
		resolution       string
		wantW, wantH     int
	}{
		{1920, 1200, "x720", 1152, 720}, {1080, 1920, "x720", 720, 1280}, {1920, 1080, "850x", 850, 478}, {2560, 1440, "x1080", 1920, 1080},
	} {
		r := RenditionSpec{Resolution: test.resolution}
		w, h, err := r.Size(test.sourceW, test.sourceH)
		if err != nil || w != test.wantW || h != test.wantH {
			t.Fatalf("%dx%d at %s => %dx%d, want %dx%d: %v", test.sourceW, test.sourceH, test.resolution, w, h, test.wantW, test.wantH, err)
		}
	}
}
