package vodupload

import (
	"fmt"
	"path"
	"strings"
)

var relayFormats = []string{"mp4", "mov", "mkv", "webm", "ts", "m2ts", "mts"}
var stagedFormats = []string{"avi", "flv", "m4v"}

const MaxFilenameBytes = 255

// UploadExtensions lists supported upload containers, without leading dots.
func UploadExtensions() []string { return append(RelayExtensions(), stagedFormats...) }

// RelayExtensions lists the containers Mist can read from an HTTP relay.
func RelayExtensions() []string { return append([]string(nil), relayFormats...) }

func containsFormat(formats []string, extension string) bool {
	extension = strings.ToLower(strings.TrimPrefix(extension, "."))
	for _, format := range formats {
		if format == extension {
			return true
		}
	}
	return false
}

// RelaySafe identifies a container whose input can read a remote object.
func RelaySafe(extension string) bool { return containsFormat(relayFormats, extension) }

// NeedsStaging identifies upload containers whose Mist input requires a local file.
func NeedsStaging(extension string) bool { return containsFormat(stagedFormats, extension) }

// ValidateFilename refuses unsupported containers before any upload is created.
func ValidateFilename(filename string) error {
	if filename == "" {
		return fmt.Errorf("filename is required")
	}
	if len(filename) > MaxFilenameBytes {
		return fmt.Errorf("filename is longer than %d UTF-8 bytes", MaxFilenameBytes)
	}
	if extension := path.Ext(filename); !RelaySafe(extension) && !NeedsStaging(extension) {
		return fmt.Errorf("unsupported filename extension; supported upload formats: %s", strings.Join(UploadExtensions(), ", "))
	}
	return nil
}
