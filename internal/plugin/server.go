package plugin

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"

	"github.com/JocelynBerrendonner/kata-vfio-pci-device-plugin/internal/cdi"
)

// kubeletSocket is the well-known UNIX socket kubelet exposes for
// device-plugin registration.
const kubeletSocket = "kubelet.sock"

// ServerConfig is the per-resource configuration.
type ServerConfig struct {
	ResourceName string // e.g. "nvidia.com/gpu"
	PluginDir    string // e.g. "/var/lib/kubelet/device-plugins"
	SocketName   string // e.g. "kata-vfio-vfio_io_pci.sock"
}

// Server is a single device plugin gRPC server backing one Kubernetes
// resource (one CDI kind).
type Server struct {
	pluginapi.UnimplementedDevicePluginServer

	cfg ServerConfig

	mu      sync.Mutex
	devices []cdi.Device
	updates chan struct{} // signals ListAndWatch to push a new snapshot

	grpcSrv *grpc.Server
	stop    chan struct{}
	done    chan struct{}
}

// NewServer constructs a Server. Call UpdateDevices() at least once
// before Start().
func NewServer(cfg ServerConfig) *Server {
	return &Server{
		cfg:     cfg,
		updates: make(chan struct{}, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

// UpdateDevices replaces the advertised device list. Safe to call at
// any time; ListAndWatch subscribers will be notified.
func (s *Server) UpdateDevices(devs []cdi.Device) {
	s.mu.Lock()
	s.devices = append(s.devices[:0:0], devs...)
	s.mu.Unlock()
	select {
	case s.updates <- struct{}{}:
	default:
	}
}

// Start binds the gRPC socket, registers with kubelet, and serves in a
// background goroutine. It returns once registration succeeds; the
// caller can then call Stop() to tear everything down.
func (s *Server) Start(ctx context.Context) error {
	sock := filepath.Join(s.cfg.PluginDir, s.cfg.SocketName)
	_ = os.Remove(sock)

	lis, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("listen %s: %w", sock, err)
	}

	s.grpcSrv = grpc.NewServer()
	pluginapi.RegisterDevicePluginServer(s.grpcSrv, s)

	go func() {
		defer close(s.done)
		if err := s.grpcSrv.Serve(lis); err != nil {
			klog.Errorf("gRPC server %s exited: %v", sock, err)
		}
	}()

	// Wait briefly for the socket to be usable before registering.
	if err := waitForSocket(sock, 5*time.Second); err != nil {
		s.grpcSrv.Stop()
		return err
	}

	if err := s.registerWithKubelet(ctx); err != nil {
		s.grpcSrv.Stop()
		return fmt.Errorf("register with kubelet: %w", err)
	}

	klog.Infof("resource %s: registered with kubelet at %s",
		s.cfg.ResourceName, sock)
	return nil
}

// Stop tears down the gRPC server.
func (s *Server) Stop() {
	if s.grpcSrv == nil {
		return
	}
	close(s.stop)
	s.grpcSrv.GracefulStop()
	<-s.done
	_ = os.Remove(filepath.Join(s.cfg.PluginDir, s.cfg.SocketName))
}

// GetDevicePluginOptions advertises which optional gRPC methods we
// implement. We do not need PreStartContainer or
// GetPreferredAllocation.
func (s *Server) GetDevicePluginOptions(_ context.Context, _ *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{
		PreStartRequired:                false,
		GetPreferredAllocationAvailable: false,
	}, nil
}

// ListAndWatch streams the current device list and pushes a fresh
// snapshot every time UpdateDevices() is called.
func (s *Server) ListAndWatch(_ *pluginapi.Empty, stream pluginapi.DevicePlugin_ListAndWatchServer) error {
	send := func() error {
		s.mu.Lock()
		out := make([]*pluginapi.Device, 0, len(s.devices))
		for _, d := range s.devices {
			out = append(out, &pluginapi.Device{
				ID:     d.Name,
				Health: pluginapi.Healthy,
			})
		}
		s.mu.Unlock()
		return stream.Send(&pluginapi.ListAndWatchResponse{Devices: out})
	}

	if err := send(); err != nil {
		return err
	}
	for {
		select {
		case <-s.stop:
			return nil
		case <-stream.Context().Done():
			return nil
		case <-s.updates:
			if err := send(); err != nil {
				return err
			}
		}
	}
}

// Allocate maps the kubelet-selected device IDs back to CDI device
// names and returns them so containerd can pick them up.
func (s *Server) Allocate(_ context.Context, req *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	s.mu.Lock()
	byName := make(map[string]string, len(s.devices))
	for _, d := range s.devices {
		byName[d.Name] = d.FullID
	}
	s.mu.Unlock()

	resp := &pluginapi.AllocateResponse{
		ContainerResponses: make([]*pluginapi.ContainerAllocateResponse, 0, len(req.ContainerRequests)),
	}
	for _, cr := range req.ContainerRequests {
		cdiDevs := make([]*pluginapi.CDIDevice, 0, len(cr.DevicesIDs))
		for _, id := range cr.DevicesIDs {
			fullID, ok := byName[id]
			if !ok {
				return nil, fmt.Errorf("allocate: unknown device %q for resource %s",
					id, s.cfg.ResourceName)
			}
			cdiDevs = append(cdiDevs, &pluginapi.CDIDevice{Name: fullID})
		}
		resp.ContainerResponses = append(resp.ContainerResponses,
			&pluginapi.ContainerAllocateResponse{CDIDevices: cdiDevs})
	}
	return resp, nil
}

func (s *Server) registerWithKubelet(ctx context.Context) error {
	kubeletAddr := filepath.Join(s.cfg.PluginDir, kubeletSocket)

	conn, err := grpc.NewClient(
		"unix:"+kubeletAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("dial %s: %w", kubeletAddr, err)
	}
	defer conn.Close()

	client := pluginapi.NewRegistrationClient(conn)
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err = client.Register(rctx, &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     s.cfg.SocketName,
		ResourceName: s.cfg.ResourceName,
		Options: &pluginapi.DevicePluginOptions{
			PreStartRequired:                false,
			GetPreferredAllocationAvailable: false,
		},
	})
	return err
}

func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("unix", path, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for socket %s", path)
}
