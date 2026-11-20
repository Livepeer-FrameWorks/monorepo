package grpc

import (
	"context"
	"testing"

	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestFoghornImportRejectsUnsupportedContainers(t *testing.T) {
	for _, test := range []struct{ filename, url string }{
		{"video.mp3", "https://media.example/video.mp3"},
		{"video.avi", "https://media.example/video.avi"},
		{"video.m3u8", "https://media.example/video.m3u8"},
	} {
		t.Run(test.filename+"/"+test.url, func(t *testing.T) {
			server, mock, done := newStatusServer(t, &fakeVodS3Client{})
			defer done()
			hash, name := "import-hash", "import-video"
			_, err := server.ImportVodAsset(context.Background(), &sharedpb.ImportVodAssetRequest{TenantId: "tenant", VodHash: &hash, InternalName: &name, Filename: test.filename, SourceUrl: test.url})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("unsupported import reached persistence: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
