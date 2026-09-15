package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
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
		if r == '"' || r == '\\' || r > 0x7e {
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

// streamChunks pipes an Agent download stream to the browser.
//
// The first chunk is read BEFORE a single header is set, and that is the whole
// point of this helper. A stream that fails on its first Recv has to answer
// with a JSON error, and a Content-Disposition set beforehand cannot be taken
// back: writeError would send the error body under `attachment`, which a real
// anchor navigation saves to disk as the file the operator asked for — a
// 40-byte "saves.zip" holding an error message, with nothing on screen to say
// so, now that no JS is watching the outcome. Once the first chunk is in hand
// the headers are honest; a failure after that can only abort the connection,
// because the body is already going out.
func streamChunks(w http.ResponseWriter, recv func() ([]byte, error), contentType, filename string) {
	first, err := recv()
	if err != nil && err != io.EOF {
		writeError(w, http.StatusBadGateway, "download failed: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", contentDisposition(filename))
	w.WriteHeader(http.StatusOK)
	if err == io.EOF { // an empty file is an honest, empty 200
		return
	}
	flusher, _ := w.(http.Flusher)
	for data := first; ; {
		if _, werr := w.Write(data); werr != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		next, rerr := recv()
		if rerr != nil {
			return
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

const maxUploadBytes = 64 << 20 // 64 MiB per request

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
func (s *Server) handleUploadFiles(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
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

	streamChunks(w, func() ([]byte, error) {
		chunk, rerr := stream.Recv()
		if rerr != nil {
			return nil, rerr
		}
		return chunk.Data, nil
	}, "application/octet-stream", path.Base(p))
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
	streamChunks(w, func() ([]byte, error) {
		chunk, rerr := stream.Recv()
		if rerr != nil {
			return nil, rerr
		}
		return chunk.Data, nil
	}, "application/zip", name)
}
