// Command release-channel classifies a release tag for the release workflow
// and prints GitHub Actions output lines:
//
//	channel=<stable|rc>
//	image_track=<latest|rc>
//
// It is a thin wrapper over pkg/version.ChannelForTag, the classifier the CLI
// also uses to resolve channels, so a tag cannot be published under one
// channel and resolved under another.
package main

import (
	"fmt"
	"os"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: release-channel <tag>")
		os.Exit(2)
	}
	out, err := classify(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "release-channel:", err)
		os.Exit(1)
	}
	fmt.Print(out)
}

func classify(tag string) (string, error) {
	channel, err := version.ChannelForTag(tag)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("channel=%s\nimage_track=%s\n", channel, channel.ImageTrack()), nil
}
