package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type ChannelUpdate struct {
	Channel  string
	Previous string
	Updated  bool
}

type channelPointer struct {
	PlatformVersion string `yaml:"platform_version"`
}

// UpdateReleaseChannels advances candidate for every release, stable for GA
// releases, and the legacy rc pointer for RC releases. Pointers are monotonic,
// so delayed or retried release jobs cannot move a channel backwards.
func UpdateReleaseChannels(gitopsDir, tag string, now time.Time) ([]ChannelUpdate, error) {
	parsed := parseTag(tag)
	if !parsed.wellFormed {
		return nil, fmt.Errorf("unsupported release tag %q", tag)
	}
	if _, err := os.Stat(manifestPath(gitopsDir, tag)); err != nil {
		return nil, fmt.Errorf("release manifest %s: %w", tag, err)
	}

	channels := []string{"candidate"}
	if parsed.isRC {
		channels = append(channels, "rc")
	} else {
		channels = append(channels, "stable")
	}

	updates := make([]ChannelUpdate, 0, len(channels))
	for _, channel := range channels {
		update, err := advanceChannel(gitopsDir, channel, parsed, now)
		if err != nil {
			return nil, err
		}
		updates = append(updates, update)
	}
	return updates, nil
}

func advanceChannel(gitopsDir, channel string, next parsedTag, now time.Time) (ChannelUpdate, error) {
	path := filepath.Join(gitopsDir, "channels", channel+".yaml")
	update := ChannelUpdate{Channel: channel}

	data, err := os.ReadFile(path)
	if err == nil {
		var current channelPointer
		if decodeErr := yaml.Unmarshal(data, &current); decodeErr != nil {
			return update, fmt.Errorf("parse %s: %w", path, decodeErr)
		}
		currentTag := parseTag(current.PlatformVersion)
		if !currentTag.wellFormed {
			return update, fmt.Errorf("parse %s: unsupported platform_version %q", path, current.PlatformVersion)
		}
		update.Previous = currentTag.raw
		if !currentTag.less(next) {
			return update, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return update, fmt.Errorf("read %s: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return update, fmt.Errorf("create channels directory: %w", err)
	}
	contents := fmt.Sprintf("platform_version: %s\nmanifest: releases/%s.yaml\nupdated_at: %s\n", next.raw, next.raw, now.UTC().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		return update, fmt.Errorf("write %s: %w", path, err)
	}
	update.Updated = true
	return update, nil
}
