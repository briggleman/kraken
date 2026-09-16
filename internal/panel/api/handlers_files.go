package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// stripUnsafeName removes what no Content-Disposition form should ever carry:
// C0/C1 control characters (CR/LF would break the header outright) and the
// Unicode bidi controls — U+200E/F, U+061C, U+202A–U+202E, U+2066–U+2069 —
// that reverse how a name renders, so "…gpj.exe" can present as "…exe.jpg" in
// a save prompt. These are dropped rather than substituted: they have no
// legitimate place in a filename, and the download-token work made the zip's
// name come from a user-controlled folder name rather than the server's.
func stripUnsafeName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			return -1
		case r == 0x061c, r == 0x200e, r == 0x200f:
			return -1
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return -1
		}
		return r
	}, name)
}

// sanitizeFilename renders a name for the Content-Disposition quoted-string:
// unsafe code points are gone, and everything a quoted-string cannot carry
// (quotes, backslashes) or a legacy client may mis-decode (anything non-ASCII)
// becomes "_". The real name still reaches modern clients through the
// RFC 5987 filename* parameter — see contentDisposition.
func sanitizeFilename(name string) string {
	ascii := strings.Map(func(r rune) rune {
		// "/" is flattened for the same reason "\" is: a filename is a leaf,
		// and a client that honours a separator in it is a client writing
		// somewhere the operator did not ask for.
		if r == '"' || r == '\\' || r == '/' || r > 0x7e {
			return '_'
		}
		return r
	}, stripUnsafeName(name))
	if ascii == "" {
		return "download"
	}
	return ascii
}

// rfc5987 percent-encodes a name as RFC 5987's ext-value, leaving only the
// attr-char set unescaped. Every non-ASCII byte is escaped, so nothing the
// header carries can be mistaken for header syntax.
func rfc5987(name string) string {
	const attr = "!#$&+-.^_`|~"
	var b strings.Builder
	for _, c := range []byte(name) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			strings.IndexByte(attr, c) >= 0:
			b.WriteByte(c)
		default:
			b.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return b.String()
}

// contentDisposition builds the attachment header for a download. It always
// carries an ASCII-only `filename=` every client understands, and adds the
// RFC 5987 percent-encoded filename* form when the real name has anything the ASCII
// form had to flatten — so a non-Latin name saves correctly without any client
// having to parse a raw UTF-8 byte in a quoted-string.
func contentDisposition(name string) string {
	safe := stripUnsafeName(name)
	ascii := sanitizeFilename(name)
	cd := `attachment; filename="` + ascii + `"`
	if safe != ascii && safe != "" {
		cd += `; filename*=UTF-8''` + rfc5987(safe)
	}
	return cd
}

// streamChunks pipes an Agent download stream to the browser. `about` is the
// identifying detail for a failure log (server id, how many paths) — never the
// path itself, and never a token.
//
// The first chunk is read BEFORE a single header is set, and that is the whole
// point of this helper. A stream that fails on its first Recv has to answer
// with a JSON error, and a Content-Disposition set beforehand cannot be taken
// back: writeError would send the error body under `attachment`, which a real
// anchor navigation saves to disk as the file the operator asked for — a
// 40-byte "saves.zip" holding an error message, with nothing on screen to say
// so, now that no JS is watching the outcome.
//
// Once the first chunk is out there is no status left to change, so a failure
// after that ABORTS THE CONNECTION (`http.ErrAbortHandler`, which chi's
// Recoverer deliberately re-panics). Returning instead would let net/http
// finish the chunked response cleanly, and a truncated file would arrive as a
// complete 200 — the browser reports a finished download and the operator has
// a half a save file with nothing anywhere saying so.
//
// recv returns each chunk's bytes plus the total size of the whole payload,
// which the Agent puts on the FIRST chunk only (0 everywhere else, and 0
// throughout for a zip, whose size is not known until it is written). A size
// greater than zero becomes Content-Length, so the browser can check the
// transfer against a number rather than trusting a clean end of stream. An
// Agent older than that field sends 0 and this behaves exactly as it did
// before: no Content-Length, detection by connection reset alone.
//
// `Accept-Ranges: none` goes out either way. Resume is structurally impossible
// here — a download token is single-use, so the second request 401s — and a
// browser that is told nothing may offer a resume that cannot work.
func (s *Server) streamChunks(w http.ResponseWriter, recv func() ([]byte, int64, error), contentType, filename string, about ...any) {
	first, size, err := recv()
	if err != nil && err != io.EOF {
		writeError(w, http.StatusBadGateway, "download failed: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", contentDisposition(filename))
	w.Header().Set("Accept-Ranges", "none")
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.WriteHeader(http.StatusOK)
	if err == io.EOF { // an empty file is an honest, empty 200
		return
	}
	// A ResponseController, not a w.(http.Flusher) assertion: every request is
	// already wrapped by the metrics and audit middleware's statusRecorder,
	// which forwards Unwrap but has no Flush of its own — so the assertion was
	// always nil in production and nothing was ever flushed.
	rc := http.NewResponseController(w)
	for data := first; ; {
		if _, werr := w.Write(data); werr != nil {
			// Over-delivery is NOT the client going away. Once Content-Length
			// is out, net/http drops everything past it and answers
			// http.ErrContentLength — so a stream that sent more than it
			// announced would otherwise "succeed" as exactly N bytes, and the
			// browser would save a truncated file as a complete download. That
			// is the failure this whole feature exists to remove, so it aborts
			// the connection like any other mid-stream betrayal.
			if errors.Is(werr, http.ErrContentLength) {
				s.logger.Warn("download exceeded its announced Content-Length",
					append(append([]any{}, about...), "announced", size)...)
				panic(http.ErrAbortHandler)
			}
			// The client went away mid-save. Nothing to abort and nothing to
			// report: the connection is already gone.
			return
		}
		if ferr := rc.Flush(); ferr != nil && !errors.Is(ferr, http.ErrNotSupported) {
			return
		}
		next, _, rerr := recv()
		if rerr == io.EOF {
			return // the whole payload is out
		}
		if rerr != nil {
			s.logger.Warn("download truncated mid-stream", append(append([]any{}, about...), "err", rerr)...)
			panic(http.ErrAbortHandler)
		}
		data = next
	}
}

type fileEntryView struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	IsDir    bool   `json:"is_dir"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified_ms"`
}

// agentForServer resolves the hosting Agent client for a server, writing the
// appropriate error response on failure.
func (s *Server) agentForServer(w http.ResponseWriter, r *http.Request, id string) (agentpb.NodeServiceClient, *store.Server, bool) {
	sv, err := s.store.GetServer(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "server not found")
		return nil, nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load server")
		return nil, nil, false
	}
	if !s.authorizeServer(w, r.Context(), sv) {
		return nil, nil, false
	}
	node, err := s.store.GetNode(r.Context(), sv.NodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load node")
		return nil, nil, false
	}
	client, err := s.nodes.Client(node.DialTarget())
	if err != nil {
		writeError(w, http.StatusBadGateway, "could not connect to agent")
		return nil, nil, false
	}
	return client, sv, true
}

// serverSlug resolves a server's game-spec slug, used by the Agent to expand
// dynamic path tokens (e.g. {{SLUG}}) in a node's backup directory. The slug is
// stable for the life of a server. Best-effort: an empty slug simply leaves
// tokens unexpanded rather than failing the backup operation.
func (s *Server) serverSlug(ctx context.Context, sv *store.Server) string {
	if sp, err := s.store.GetSpec(ctx, sv.SpecID); err == nil {
		return sp.Slug
	}
	return ""
}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	client, sv, ok := s.agentForServer(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	resp, err := client.ListFiles(ctx, &agentpb.ListFilesRequest{ServerId: sv.ID, Path: r.URL.Query().Get("path")})
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent error: "+err.Error())
		return
	}
	views := make([]fileEntryView, 0, len(resp.Entries))
	for _, e := range resp.Entries {
		views = append(views, fileEntryView{Name: e.Name, Path: e.Path, IsDir: e.IsDir, Size: e.Size, Modified: e.ModUnixMs})
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": resp.Path, "entries": views})
}

// maxEditBytes caps the size of a file the in-browser editor will load. Larger
// files are reported as too-large rather than streamed into the editor.
const maxEditBytes = 1 << 20 // 1 MiB

// handleReadFile returns a single file's contents for the in-browser editor.
func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	client, sv, ok := s.agentForServer(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	resp, err := client.ReadFile(ctx, &agentpb.ReadFileRequest{ServerId: sv.ID, Path: p, MaxBytes: maxEditBytes})
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent error: "+err.Error())
		return
	}
	tooLarge := resp.Truncated || resp.Size > maxEditBytes
	out := map[string]any{
		"path":      p,
		"size":      resp.Size,
		"is_binary": resp.IsBinary,
		"too_large": tooLarge,
		"content":   "",
	}
	// Only hand back text the editor can safely load: not binary, not oversized.
	if !resp.IsBinary && !tooLarge {
		out["content"] = string(resp.Content)
	}
	writeJSON(w, http.StatusOK, out)
}

const (
	// maxUploadBytes bounds one upload request's payload.
	maxUploadBytes = 64 << 20 // 64 MiB per request
	// maxUploadOverhead is the slack the hard body cap allows on top of it for
	// multipart framing — boundaries and part headers, which are the client's
	// to choose and are not the file.
	maxUploadOverhead = 1 << 20 // 1 MiB
	// maxUploadMemory is how much of a multipart body is parsed in RAM; the
	// rest spills to temp files, which is why the body itself is capped above.
	maxUploadMemory = 8 << 20 // 8 MiB
)

type mkdirRequest struct {
	Path string `json:"path"`
}

func (s *Server) handleMakeDir(w http.ResponseWriter, r *http.Request) {
	var req mkdirRequest
	if err := decodeJSON(r, &req); err != nil || req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	client, sv, ok := s.agentForServer(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if _, err := client.MakeDir(ctx, &agentpb.MakeDirRequest{ServerId: sv.ID, Path: req.Path}); err != nil {
		writeError(w, http.StatusBadGateway, "agent error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "created"})
}

type movePathRequest struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

func (s *Server) handleMovePath(w http.ResponseWriter, r *http.Request) {
	var req movePathRequest
	if err := decodeJSON(r, &req); err != nil || req.Src == "" || req.Dst == "" {
		writeError(w, http.StatusBadRequest, "src and dst are required")
		return
	}
	client, sv, ok := s.agentForServer(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if _, err := client.MovePath(ctx, &agentpb.MovePathRequest{ServerId: sv.ID, Src: req.Src, Dst: req.Dst}); err != nil {
		writeError(w, http.StatusBadGateway, "agent error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "moved"})
}

func (s *Server) handleCopyPath(w http.ResponseWriter, r *http.Request) {
	var req movePathRequest
	if err := decodeJSON(r, &req); err != nil || req.Src == "" || req.Dst == "" {
		writeError(w, http.StatusBadRequest, "src and dst are required")
		return
	}
	client, sv, ok := s.agentForServer(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if _, err := client.CopyPath(ctx, &agentpb.CopyPathRequest{ServerId: sv.ID, Src: req.Src, Dst: req.Dst}); err != nil {
		writeError(w, http.StatusBadGateway, "agent error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "copied"})
}

type writeFileRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (s *Server) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	var req writeFileRequest
	if err := decodeJSON(r, &req); err != nil || req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	client, sv, ok := s.agentForServer(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if _, err := client.WriteFile(ctx, &agentpb.WriteFileRequest{ServerId: sv.ID, Path: req.Path, Content: []byte(req.Content)}); err != nil {
		writeError(w, http.StatusBadGateway, "agent error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "written"})
}

// handleUploadFiles accepts multipart uploads into the directory given by ?path=.
//
// The body is capped before it is parsed. ParseMultipartForm's argument is only
// the in-memory threshold — everything past it spills to temp files, without
// limit — so the only actual bound on an authenticated upload is this reader:
// without it one request could fill the Panel's disk. The slack over
// maxUploadBytes covers multipart framing (boundaries, part headers) so a
// legitimate upload of exactly the limit is not refused for its own envelope.
func (s *Server) handleUploadFiles(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+maxUploadOverhead)
	// #nosec G120 -- the body is bounded by the MaxBytesReader on the line
	// above; the argument here is only the in-memory threshold. G120 flags the
	// call site and cannot see the wrapper.
	if err := r.ParseMultipartForm(maxUploadMemory); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "upload is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid upload")
		return
	}
	dir := r.URL.Query().Get("path")
	if dir == "" {
		dir = "/data"
	}
	client, sv, ok := s.agentForServer(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		writeError(w, http.StatusBadRequest, "no files provided")
		return
	}
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			writeError(w, http.StatusBadRequest, "could not read upload")
			return
		}
		data, err := io.ReadAll(io.LimitReader(f, maxUploadBytes))
		f.Close()
		if err != nil {
			writeError(w, http.StatusBadRequest, "could not read upload")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		_, err = client.WriteFile(ctx, &agentpb.WriteFileRequest{ServerId: sv.ID, Path: dir + "/" + fh.Filename, Content: data})
		cancel()
		if err != nil {
			writeError(w, http.StatusBadGateway, "agent error: "+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"uploaded": len(files)})
}

type deleteFilesRequest struct {
	Paths []string `json:"paths"`
}

func (s *Server) handleDeleteFiles(w http.ResponseWriter, r *http.Request) {
	var req deleteFilesRequest
	if err := decodeJSON(r, &req); err != nil || len(req.Paths) == 0 {
		writeError(w, http.StatusBadRequest, "paths are required")
		return
	}
	client, sv, ok := s.agentForServer(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if _, err := client.DeletePaths(ctx, &agentpb.DeletePathsRequest{ServerId: sv.ID, Paths: req.Paths}); err != nil {
		writeError(w, http.StatusBadGateway, "agent error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleDownloadFile streams a single file's raw bytes (not a zip) to the browser.
func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	client, sv, ok := s.agentForServer(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	stream, err := client.DownloadFile(ctx, &agentpb.DownloadFileRequest{ServerId: sv.ID, Path: p})
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent error: "+err.Error())
		return
	}

	s.streamChunks(w, func() ([]byte, int64, error) {
		chunk, rerr := stream.Recv()
		if rerr != nil {
			return nil, 0, rerr
		}
		// Size rides on the first chunk and is 0 thereafter; an Agent older
		// than that field sends 0 throughout, which simply means "unknown".
		return chunk.Data, chunk.Size, nil
	}, "application/octet-stream", path.Base(p), "server", sv.ID, "paths", 1)
}

type downloadFilesRequest struct {
	Paths []string `json:"paths"`
}

// handleDownloadFiles streams a zip of the selected paths from the Agent straight
// to the browser. The paths ride in a JSON body, so this is the session-
// authenticated route; its GET twin takes them from a download token instead
// (handleDownloadFilesByToken).
func (s *Server) handleDownloadFiles(w http.ResponseWriter, r *http.Request) {
	var req downloadFilesRequest
	if err := decodeJSON(r, &req); err != nil || len(req.Paths) == 0 {
		writeError(w, http.StatusBadRequest, "paths are required")
		return
	}
	// The POST route's zip is named after the server, as it always has been.
	s.streamZip(w, r, req.Paths, false)
}

// handleDownloadFilesByToken is the zip route a plain <a download href> can
// reach: there is no body, so the paths come from the one-time token the
// dispatcher already redeemed and bound into the request (see
// handlers_filedownloadtoken.go).
func (s *Server) handleDownloadFilesByToken(w http.ResponseWriter, r *http.Request) {
	paths := downloadPathsFrom(r.Context())
	if len(paths) == 0 {
		writeError(w, http.StatusBadRequest, "paths are required")
		return
	}
	// A tokenised download is a real navigation, so Content-Disposition is the
	// only thing that names the save — a one-path zip is named after that path,
	// which is what the Files tab's pill has always shown for a folder.
	s.streamZip(w, r, paths, true)
}

// streamZip asks the hosting Agent for a zip of paths and pipes it to the
// browser as it arrives — the Panel never holds the archive.
func (s *Server) streamZip(w http.ResponseWriter, r *http.Request, paths []string, nameAfterPath bool) {
	client, sv, ok := s.agentForServer(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	stream, err := client.DownloadFiles(ctx, &agentpb.DownloadFilesRequest{ServerId: sv.ID, Paths: paths})
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent error: "+err.Error())
		return
	}

	// Both halves of the name are attacker-adjacent — the server name is
	// operator-supplied and the folder name is whoever can write to the tree —
	// so both go through contentDisposition's sanitizer (see streamChunks).
	name := sv.Name + "-files.zip"
	if nameAfterPath && len(paths) == 1 {
		if base := path.Base(paths[0]); base != "" && base != "/" && base != "." {
			name = base + ".zip"
		}
	}
	s.streamChunks(w, func() ([]byte, int64, error) {
		chunk, rerr := stream.Recv()
		if rerr != nil {
			return nil, 0, rerr
		}
		// A zip carries no size, and this hardcodes that rather than forwarding
		// chunk.Size: the archive is written as it streams, so nothing on
		// either side could enforce a length announced up front. Making it
		// structural means a future Agent that sets the field on a zip cannot
		// turn this route into a Content-Length it does not keep.
		return chunk.Data, 0, nil
	}, "application/zip", name, "server", sv.ID, "paths", len(paths))
}
