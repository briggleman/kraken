package agent

import (
	"context"
	"io"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// Service adapts the gRPC NodeService surface onto a Runtime. It is transport
// only: every method delegates to the runtime, translating streaming RPCs into
// the runtime's emit-callback form.
type Service struct {
	agentpb.UnimplementedNodeServiceServer
	rt Runtime
}

// NewService wraps a Runtime as a gRPC NodeService implementation.
func NewService(rt Runtime) *Service { return &Service{rt: rt} }

func (s *Service) GetNodeInfo(ctx context.Context, _ *agentpb.GetNodeInfoRequest) (*agentpb.NodeInfo, error) {
	return s.rt.NodeInfo(ctx)
}

func (s *Service) CreateServer(ctx context.Context, req *agentpb.CreateServerRequest) (*agentpb.CreateServerResponse, error) {
	if req.Spec == nil {
		return nil, status.Error(codes.InvalidArgument, "spec is required")
	}
	if err := s.rt.Create(ctx, req.Spec); err != nil {
		return nil, err
	}
	return &agentpb.CreateServerResponse{}, nil
}

func (s *Service) RemoveServer(ctx context.Context, req *agentpb.RemoveServerRequest) (*agentpb.RemoveServerResponse, error) {
	if err := s.rt.Remove(ctx, req.ServerId, req.DeleteData); err != nil {
		return nil, err
	}
	return &agentpb.RemoveServerResponse{}, nil
}

func (s *Service) ListFiles(ctx context.Context, req *agentpb.ListFilesRequest) (*agentpb.ListFilesResponse, error) {
	entries, err := s.rt.ListFiles(ctx, req.ServerId, req.Path)
	if err != nil {
		return nil, err
	}
	return &agentpb.ListFilesResponse{Path: req.Path, Entries: entries}, nil
}

func (s *Service) DownloadFiles(req *agentpb.DownloadFilesRequest, stream agentpb.NodeService_DownloadFilesServer) error {
	pr, pw := io.Pipe()
	go func() {
		err := s.rt.ZipFiles(stream.Context(), req.ServerId, req.Paths, pw)
		_ = pw.CloseWithError(err)
	}()
	buf := make([]byte, 64*1024)
	for {
		n, err := pr.Read(buf)
		if n > 0 {
			if serr := stream.Send(&agentpb.FileChunk{Data: buf[:n]}); serr != nil {
				_ = pr.CloseWithError(serr)
				return serr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (s *Service) ReadFile(ctx context.Context, req *agentpb.ReadFileRequest) (*agentpb.ReadFileResponse, error) {
	content, size, truncated, binary, err := s.rt.ReadFile(ctx, req.ServerId, req.Path, req.MaxBytes)
	if err != nil {
		return nil, err
	}
	return &agentpb.ReadFileResponse{Content: content, Size: size, Truncated: truncated, IsBinary: binary}, nil
}

func (s *Service) DownloadFile(req *agentpb.DownloadFileRequest, stream agentpb.NodeService_DownloadFileServer) error {
	pr, pw := io.Pipe()
	go func() {
		err := s.rt.DownloadFile(stream.Context(), req.ServerId, req.Path, pw)
		_ = pw.CloseWithError(err)
	}()
	buf := make([]byte, 64*1024)
	for {
		n, err := pr.Read(buf)
		if n > 0 {
			if serr := stream.Send(&agentpb.FileChunk{Data: buf[:n]}); serr != nil {
				_ = pr.CloseWithError(serr)
				return serr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (s *Service) MovePath(ctx context.Context, req *agentpb.MovePathRequest) (*agentpb.MovePathResponse, error) {
	if err := s.rt.MovePath(ctx, req.ServerId, req.Src, req.Dst); err != nil {
		return nil, err
	}
	return &agentpb.MovePathResponse{}, nil
}

func (s *Service) CopyPath(ctx context.Context, req *agentpb.CopyPathRequest) (*agentpb.CopyPathResponse, error) {
	if err := s.rt.CopyPath(ctx, req.ServerId, req.Src, req.Dst); err != nil {
		return nil, err
	}
	return &agentpb.CopyPathResponse{}, nil
}

func (s *Service) MakeDir(ctx context.Context, req *agentpb.MakeDirRequest) (*agentpb.MakeDirResponse, error) {
	if err := s.rt.MakeDir(ctx, req.ServerId, req.Path); err != nil {
		return nil, err
	}
	return &agentpb.MakeDirResponse{}, nil
}

func (s *Service) WriteFile(ctx context.Context, req *agentpb.WriteFileRequest) (*agentpb.WriteFileResponse, error) {
	if err := s.rt.WriteFile(ctx, req.ServerId, req.Path, req.Content); err != nil {
		return nil, err
	}
	return &agentpb.WriteFileResponse{}, nil
}

func (s *Service) DeletePaths(ctx context.Context, req *agentpb.DeletePathsRequest) (*agentpb.DeletePathsResponse, error) {
	if err := s.rt.DeletePaths(ctx, req.ServerId, req.Paths); err != nil {
		return nil, err
	}
	return &agentpb.DeletePathsResponse{}, nil
}

func (s *Service) CreateBackup(ctx context.Context, req *agentpb.CreateBackupRequest) (*agentpb.BackupInfo, error) {
	return s.rt.CreateBackup(ctx, req.ServerId, req.Slug, req.Name)
}

func (s *Service) ListBackups(ctx context.Context, req *agentpb.ListBackupsRequest) (*agentpb.ListBackupsResponse, error) {
	backups, err := s.rt.ListBackups(ctx, req.ServerId, req.Slug)
	if err != nil {
		return nil, err
	}
	return &agentpb.ListBackupsResponse{Backups: backups}, nil
}

func (s *Service) RestoreBackup(ctx context.Context, req *agentpb.RestoreBackupRequest) (*agentpb.RestoreBackupResponse, error) {
	if err := s.rt.RestoreBackup(ctx, req.ServerId, req.Slug, req.Id); err != nil {
		return nil, err
	}
	return &agentpb.RestoreBackupResponse{}, nil
}

func (s *Service) DeleteBackup(ctx context.Context, req *agentpb.DeleteBackupRequest) (*agentpb.DeleteBackupResponse, error) {
	if err := s.rt.DeleteBackup(ctx, req.ServerId, req.Slug, req.Id); err != nil {
		return nil, err
	}
	return &agentpb.DeleteBackupResponse{}, nil
}

func (s *Service) ApplyConfig(ctx context.Context, req *agentpb.ApplyConfigRequest) (*agentpb.ApplyConfigResponse, error) {
	files := make(map[string]string, len(req.Files))
	for _, f := range req.Files {
		files[f.Path] = f.Content
	}
	if err := s.rt.ApplyConfig(ctx, req.ServerId, files); err != nil {
		return nil, err
	}
	return &agentpb.ApplyConfigResponse{}, nil
}

func (s *Service) InstallServer(req *agentpb.InstallServerRequest, stream agentpb.NodeService_InstallServerServer) error {
	return s.rt.Install(stream.Context(), req, stream.Send)
}

func (s *Service) PowerAction(ctx context.Context, req *agentpb.PowerActionRequest) (*agentpb.PowerActionResponse, error) {
	state, err := s.rt.Power(ctx, req.ServerId, req.Action)
	if err != nil {
		return nil, err
	}
	return &agentpb.PowerActionResponse{State: state}, nil
}

func (s *Service) GetServerStatus(ctx context.Context, req *agentpb.GetServerStatusRequest) (*agentpb.ServerStatus, error) {
	return s.rt.Status(ctx, req.ServerId)
}

func (s *Service) StreamConsole(req *agentpb.StreamConsoleRequest, stream agentpb.NodeService_StreamConsoleServer) error {
	return s.rt.StreamConsole(stream.Context(), req.ServerId, req.TailLines, stream.Send)
}

func (s *Service) SendCommand(ctx context.Context, req *agentpb.SendCommandRequest) (*agentpb.SendCommandResponse, error) {
	if err := s.rt.SendCommand(ctx, req.ServerId, req.Command); err != nil {
		return nil, err
	}
	return &agentpb.SendCommandResponse{}, nil
}

func (s *Service) StreamStats(req *agentpb.StreamStatsRequest, stream agentpb.NodeService_StreamStatsServer) error {
	return s.rt.StreamStats(stream.Context(), req.ServerId, req.IntervalMs, stream.Send)
}

func (s *Service) ApplyNodeConfig(ctx context.Context, req *agentpb.ApplyNodeConfigRequest) (*agentpb.ApplyNodeConfigResponse, error) {
	ok, detail := s.rt.ApplyNodeConfig(ctx, req.Config)
	return &agentpb.ApplyNodeConfigResponse{Ok: ok, Detail: detail}, nil
}

func (s *Service) ReplicateBackups(ctx context.Context, req *agentpb.ReplicateBackupsRequest) (*agentpb.ReplicateBackupsResponse, error) {
	mirrored, skipped, err := s.rt.ReplicateBackups(ctx, req.ServerId, req.Slug)
	if err != nil {
		return nil, err
	}
	return &agentpb.ReplicateBackupsResponse{Mirrored: mirrored, Skipped: skipped}, nil
}
