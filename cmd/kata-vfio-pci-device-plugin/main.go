// Command kata-vfio-pci-device-plugin is a Kubernetes device plugin that
// advertises VFIO-bound PCI devices (described by CDI specs under
// /etc/cdi/) as allocatable resources. Designed for Kata-isolated pods
// that need raw PCI passthrough into the UVM.
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/klog/v2"

	"github.com/JocelynBerrendonner/kata-vfio-pci-device-plugin/internal/plugin"
)

func main() {
	klog.InitFlags(nil)

	cdiDir := flag.String("cdi-dir", "/etc/cdi",
		"Directory containing CDI spec YAML files to expose.")
	pluginDir := flag.String("plugin-dir", "/var/lib/kubelet/device-plugins",
		"Kubelet device-plugin socket directory.")
	resourcePrefix := flag.String("resource-prefix", "vfio.io",
		"Kubernetes resource group used for advertised resources. "+
			"Each CDI kind 'vendor/class' becomes '<prefix>/class'. "+
			"Set empty to derive the prefix from the CDI kind's vendor "+
			"(e.g. 'vfio/gpu' -> 'vfio.io/gpu').")
	kindFilter := flag.String("kind-filter", "vfio/*",
		"Glob filter (comma-separated) over CDI kinds to expose. "+
			"Default exposes any kind under the 'vfio' vendor.")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		klog.Infof("received signal %s, shutting down", sig)
		cancel()
	}()

	mgr := plugin.NewManager(plugin.Config{
		CDIDir:         *cdiDir,
		PluginDir:      *pluginDir,
		ResourcePrefix: *resourcePrefix,
		KindFilter:     *kindFilter,
	})

	if err := mgr.Run(ctx); err != nil {
		klog.Exitf("manager exited with error: %v", err)
	}
	klog.Info("clean shutdown")
}
