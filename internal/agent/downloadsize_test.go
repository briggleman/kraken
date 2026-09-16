package agent_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// A single-file download announces its total on the FIRST chunk and nothing
// after it. That number is what lets the Panel set Content-Length, which is the
// only way a browser can tell a truncated save from a complete one — the
// alternative is trusting a clean end of stream, which a dropped connection
// looks exactly like.
func TestDownloadFileAnnouncesItsSizeOnTheFirstChunkOnly(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	// Bigger than the Agent's 64 KiB read buffer, so the stream is genuinely
	// several chunks and "first chunk only" means something.
	body := bytes.Repeat([]byte("kraken!"), 40_000) // 280,000 bytes
	if _, err := c.WriteFile(ctx, &agentpb.WriteFileRequest{
		ServerId: "s1", Path: "/data/big.bin", Content: body,
	}); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	stream, err := c.DownloadFile(ctx, &agentpb.DownloadFileRequest{ServerId: "s1", Path: "/data/big.bin"})
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	var got bytes.Buffer
	chunks := 0
	for {
		chunk, rerr := stream.Recv()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			t.Fatalf("recv: %v", rerr)
		}
		chunks++
		if chunks == 1 {
			if chunk.Size != int64(len(body)) {
				t.Fatalf("first chunk announced %d bytes, want the file's %d", chunk.Size, len(body))
			}
		} else if chunk.Size != 0 {
			t.Fatalf("chunk %d carried size %d, want 0 — the total rides on the first chunk alone", chunks, chunk.Size)
		}
		got.Write(chunk.Data)
	}
	if chunks < 2 {
		t.Fatalf("the payload arrived in %d chunk(s); the test needs a multi-chunk stream to mean anything", chunks)
	}
	if !bytes.Equal(got.Bytes(), body) {
		t.Fatalf("streamed %d bytes, want the file's %d", got.Len(), len(body))
	}
}

// A zip's size is not known until it has been written, so DownloadFiles
// announces nothing and the Panel sends no Content-Length for it. Same shape as
// an Agent older than the field, which is exactly why the Panel treats 0 as
// "unknown" rather than as an error.
func TestDownloadFilesZipAnnouncesNoSize(t *testing.T) {
	c := newClient(t)
	stream, err := c.DownloadFiles(context.Background(), &agentpb.DownloadFilesRequest{
		ServerId: "s1", Paths: []string{"/data/saves"},
	})
	if err != nil {
		t.Fatalf("DownloadFiles: %v", err)
	}
	for {
		chunk, rerr := stream.Recv()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			t.Fatalf("recv: %v", rerr)
		}
		if chunk.Size != 0 {
			t.Fatalf("a zip chunk announced %d bytes; the archive's size is not known until it is written", chunk.Size)
		}
	}
}
