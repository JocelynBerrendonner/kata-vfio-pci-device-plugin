// Package plugin implements the Kubernetes device plugin (one gRPC
// server per advertised resource) and a manager that wires it up to
// the CDI spec directory.
package plugin

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"k8s.io/klog/v2"

	"github.com/JocelynBerrendonner/kata-vfio-pci-device-plugin/internal/cdi"
)

// Config holds the runtime configuration assembled from CLI flags.
type Config struct {
	CDIDir         string
	PluginDir      string
	ResourcePrefix string
	KindFilter     string
}

// Manager owns the set of per-resource device plugin servers and
// reacts to changes under CDIDir by adding/removing/refreshing them.
type Manager struct {
	cfg Config

	mu      sync.Mutex
	servers map[string]*Server // keyed by resource name (e.g. "vfio.io/gpu")
}

// NewManager constructs a Manager. It does not start anything yet.
func NewManager(cfg Config) *Manager {
	return &Manager{
		cfg:     cfg,
		servers: make(map[string]*Server),
	}
}

// Run blocks until ctx is cancelled. It performs an initial CDI scan,
// starts one plugin server per matching kind, and then watches the
// CDI directory for changes, reconciling on every event.
func (m *Manager) Run(ctx context.Context) error {
	if err := m.reconcile(ctx); err != nil {
		return fmt.Errorf("initial reconcile: %w", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fsnotify: %w", err)
	}
	defer watcher.Close()

	if err := watcher.Add(m.cfg.CDIDir); err != nil {
		klog.Warningf("could not watch %s (%v); falling back to periodic rescan",
			m.cfg.CDIDir, err)
	}

	// Coalesce rapid bursts of fs events into one reconcile.
	const debounce = 500 * time.Millisecond
	var pending *time.Timer

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			return nil

		case ev, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			klog.V(4).Infof("fsnotify event: %s", ev)
			if pending != nil {
				pending.Stop()
			}
			pending = time.AfterFunc(debounce, func() {
				if err := m.reconcile(ctx); err != nil {
					klog.Errorf("reconcile after fs event: %v", err)
				}
			})

		case err := <-watcher.Errors:
			if err != nil {
				klog.Warningf("fsnotify error: %v", err)
			}

		case <-ticker.C:
			if err := m.reconcile(ctx); err != nil {
				klog.Errorf("periodic reconcile: %v", err)
			}
		}
	}
}

// reconcile reads the current CDI snapshot and brings the set of
// running plugin servers in line with it.
func (m *Manager) reconcile(ctx context.Context) error {
	snap, err := cdi.Read(m.cfg.CDIDir)
	if err != nil {
		return err
	}

	wanted := make(map[string][]cdi.Device, len(snap.ByKind))
	for kind, devs := range snap.ByKind {
		if !cdi.KindMatches(kind, m.cfg.KindFilter) {
			continue
		}
		res := resourceName(m.cfg.ResourcePrefix, kind)
		wanted[res] = devs
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Stop servers whose resource is no longer wanted.
	for res, srv := range m.servers {
		if _, keep := wanted[res]; !keep {
			klog.Infof("resource %s disappeared, stopping server", res)
			srv.Stop()
			delete(m.servers, res)
		}
	}

	// Start servers for newly seen resources, refresh existing ones.
	for res, devs := range wanted {
		if srv, ok := m.servers[res]; ok {
			srv.UpdateDevices(devs)
			continue
		}

		klog.Infof("resource %s appeared with %d device(s), starting server",
			res, len(devs))
		srv := NewServer(ServerConfig{
			ResourceName: res,
			PluginDir:    m.cfg.PluginDir,
			SocketName:   socketNameFor(res),
		})
		srv.UpdateDevices(devs)
		if err := srv.Start(ctx); err != nil {
			klog.Errorf("start server for %s: %v", res, err)
			continue
		}
		m.servers[res] = srv
	}

	return nil
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for res, srv := range m.servers {
		klog.Infof("stopping server for %s", res)
		srv.Stop()
		delete(m.servers, res)
	}
}

// resourceName maps a CDI kind ("vendor/class") to the Kubernetes
// resource name. If prefix is non-empty, it is used as the resource
// group; otherwise it is derived from the CDI vendor.
//
// Examples:
//
//	prefix="vfio.io", kind="vfio/gpu"  -> "vfio.io/gpu"
//	prefix="vfio.io", kind="vfio/ib"   -> "vfio.io/ib"
//	prefix="",        kind="acme/widget" -> "acme.io/widget"
func resourceName(prefix, kind string) string {
	slash := strings.IndexByte(kind, '/')
	if slash < 0 {
		// Malformed kind; expose under prefix/<whole-kind> as a fallback.
		if prefix == "" {
			return "kata-vfio.io/" + kind
		}
		return prefix + "/" + kind
	}
	vendor, class := kind[:slash], kind[slash+1:]
	if prefix == "" {
		prefix = vendor + ".io"
	}
	return prefix + "/" + class
}

// socketNameFor derives a stable, kubelet-visible socket filename for
// a given resource. Kubelet picks up any *.sock under PluginDir, and
// the filename has to be unique per process.
func socketNameFor(res string) string {
	clean := strings.NewReplacer("/", "_", ".", "_").Replace(res)
	return "kata-vfio-" + clean + ".sock"
}
