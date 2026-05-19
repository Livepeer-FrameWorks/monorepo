package control

import "testing"

func TestNormalizeThumbnailVTTReferences(t *testing.T) {
	input := `WEBVTT

00:00:00.000 --> 00:00:04.000
/processing+abc123.jpg?track=3#xywh=0,0,120,90

00:00:04.000 --> 00:00:08.000
/live+stream.jpg?track=7#xywh=120,0,120,90
`

	got := normalizeThumbnailVTTReferences(input)
	want := `WEBVTT

00:00:00.000 --> 00:00:04.000
sprite.jpg#xywh=0,0,120,90

00:00:04.000 --> 00:00:08.000
sprite.jpg#xywh=120,0,120,90
`
	if got != want {
		t.Fatalf("normalizeThumbnailVTTReferences() = %q, want %q", got, want)
	}
}
