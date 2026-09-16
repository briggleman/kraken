package agent_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/briggleman/kraken/internal/agent"
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

// misstatingRuntime reports a size that disagrees with what it then streams —
// which is exactly what a live file does between the stat and the read. delta
// is added to the true size: negative means the file grew after we measured it,
// positive means it shrank.
type misstatingRuntime struct {
	*agent.FakeRuntime
	delta int64
}

func (m *misstatingRuntime) StatFile(ctx context.Context, serverID, p string) (int64, error) {
	n, err := m.FakeRuntime.StatFile(ctx, serverID, p)
	if err != nil {
		return 0, err
	}
	return n + m.delta, nil
}

func drain(t *testing.T, stream agentpb.NodeService_DownloadFileClient) (body []byte, announced int64, err error) {
	t.Helper()
	first := true
	for {
		chunk, rerr := stream.Recv()
		if rerr == io.EOF {
			return body, announced, nil
		}
		if rerr != nil {
			return body, announced, rerr
		}
		if first {
			announced = chunk.Size
			first = false
		}
		body = append(body, chunk.Data...)
	}
}

// A file that GREW since the stat is the ordinary case, not an attack: a
// running game server appends to the log or the save the operator is
// downloading. The stream stops at exactly the announced length and ends
// cleanly, so the download is a consistent prefix — which is all
// Content-Length ever promised. Failing the whole transfer here would break
// downloading a live server's log, the primary thing this is used for.
func TestDownloadFileTruncatesAFileThatGrew(t *testing.T) {
	rt := &misstatingRuntime{FakeRuntime: agent.NewFakeRuntime("abyss-node-01", "linux", true, "test"), delta: -200_000}
	c := newClientFor(t, rt)
	ctx := context.Background()

	body := bytes.Repeat([]byte("kraken!"), 40_000) // 280,000 bytes on disk
	if _, err := c.WriteFile(ctx, &agentpb.WriteFileRequest{ServerId: "s1", Path: "/data/growing.log", Content: body}); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	stream, err := c.DownloadFile(ctx, &agentpb.DownloadFileRequest{ServerId: "s1", Path: "/data/growing.log"})
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	got, announced, rerr := drain(t, stream)
	if rerr != nil {
		t.Fatalf("a file that grew mid-stream failed the download: %v", rerr)
	}
	want := int64(len(body)) - 200_000
	if announced != want {
		t.Fatalf("announced %d bytes, want the size at stat time (%d)", announced, want)
	}
	if int64(len(got)) != want {
		t.Fatalf("delivered %d bytes against an announced %d — Content-Length would not match the body", len(got), want)
	}
	if !bytes.Equal(got, body[:want]) {
		t.Fatal("the truncated download is not a prefix of the file")
	}
}

// A file that SHRANK cannot deliver what was announced, so the RPC fails and
// the Panel aborts the connection. A browser reads a reset as "this download
// did not finish"; a short body under a longer Content-Length it would simply
// save.
func TestDownloadFileFailsWhenTheFileShrank(t *testing.T) {
	rt := &misstatingRuntime{FakeRuntime: agent.NewFakeRuntime("abyss-node-01", "linux", true, "test"), delta: 500}
	c := newClientFor(t, rt)
	stream, err := c.DownloadFile(context.Background(), &agentpb.DownloadFileRequest{ServerId: "s1", Path: "/data/server.cfg"})
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	if _, _, rerr := drain(t, stream); rerr == nil {
		t.Fatal("a stream that ended short of its announced size succeeded — the Panel would serve a body that does not match its own header")
	}
}
