package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// sanitizeFilename strips characters that could break the Content-Disposition
// quoted-string (quotes, backslashes, and control chars including CR/LF).
func sanitizeFilename(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '"' || r == '\\' || r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, name)
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
	client, err := s.nodes.Client(node.Address)
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

	// Sanitize the filename for the Content-Disposition quoted-string (strip
	// quotes/backslashes/control chars so a crafted name can't break the header).
	name := sanitizeFilename(path.Base(p))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	flusher, _ := w.(http.Flusher)

	wroteHeader := false
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			return
		}
		if err != nil {
			if !wroteHeader {
				writeError(w, http.StatusBadGateway, "download failed: "+err.Error())
			}
			return
		}
		if !wroteHeader {
			w.WriteHeader(http.StatusOK)
			wroteHeader = true
		}
		if _, werr := w.Write(chunk.Data); werr != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

type downloadFilesRequest struct {
	Paths []string `json:"paths"`
}

// handleDownloadFiles streams a zip of the selected paths from the Agent straight
// to the browser.
func (s *Server) handleDownloadFiles(w http.ResponseWriter, r *http.Request) {
	var req downloadFilesRequest
	if err := decodeJSON(r, &req); err != nil || len(req.Paths) == 0 {
		writeError(w, http.StatusBadRequest, "paths are required")
		return
	}
	client, sv, ok := s.agentForServer(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	stream, err := client.DownloadFiles(ctx, &agentpb.DownloadFilesRequest{ServerId: sv.ID, Paths: req.Paths})
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent error: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+sv.Name+`-files.zip"`)
	flusher, _ := w.(http.Flusher)

	wroteHeader := false
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			return
		}
		if err != nil {
			// If nothing was written yet, surface an error status; otherwise the
			// zip is already partly streamed and we can only abort the connection.
			if !wroteHeader {
				writeError(w, http.StatusBadGateway, "download failed: "+err.Error())
			}
			return
		}
		if !wroteHeader {
			w.WriteHeader(http.StatusOK)
			wroteHeader = true
		}
		if _, werr := w.Write(chunk.Data); werr != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}
