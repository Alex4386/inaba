package host

import (
	"context"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	psHost "github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/version"
)

// GetSystemInfo answers SystemService: what the machine is and how busy it is. What
// cannot be read on this system is left at zero rather than failing the rest.
func (h *Host) GetSystemInfo(ctx context.Context, _ *inabav1.GetSystemInfoRequest) (*inabav1.SystemInfo, error) {
	info := &inabav1.SystemInfo{Arch: runtime.GOARCH, InabaVersion: version.String()}
	if hi, err := psHost.InfoWithContext(ctx); err == nil {
		info.Hostname, info.Kernel, info.UptimeSeconds = hi.Hostname, hi.KernelVersion, hi.Uptime
		info.Platform = hi.Platform + " " + hi.PlatformVersion
	}
	if cs, err := cpu.InfoWithContext(ctx); err == nil && len(cs) > 0 {
		info.CpuModel = cs[0].ModelName
	}
	if n, err := cpu.CountsWithContext(ctx, true); err == nil {
		info.CpuCores = int32(n)
	}
	// A short sample: a reading since the last call would be meaningless on the first.
	if p, err := cpu.PercentWithContext(ctx, 250*time.Millisecond, false); err == nil && len(p) > 0 {
		info.CpuPercent = p[0]
	}
	if l, err := load.AvgWithContext(ctx); err == nil {
		info.Load1, info.Load5, info.Load15 = l.Load1, l.Load5, l.Load15
	}
	if m, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		info.MemoryTotal, info.MemoryUsed = m.Total, m.Used
	}
	dir := h.dataDir
	if dir == "" {
		dir = "/"
	}
	if d, err := disk.UsageWithContext(ctx, dir); err == nil {
		info.DiskTotal, info.DiskUsed = d.Total, d.Used
	}
	// A thread takes h.mu while holding its own lock, so its status is read after h.mu is let go.
	h.mu.Lock()
	threads := make([]*thread, 0, len(h.threads))
	for _, t := range h.threads {
		threads = append(threads, t)
	}
	h.mu.Unlock()
	for _, t := range threads {
		info.Threads++
		if t.snapshot().Status == inabav1.ThreadStatus_THREAD_STATUS_RUNNING {
			info.ThreadsRunning++
		}
	}
	return info, nil
}
