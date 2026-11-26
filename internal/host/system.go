package host

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect/v2"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	psHost "github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"

	inabav1 "github.com/Alex4386/inaba/gen/inaba/v1"
	"github.com/Alex4386/inaba/internal/rpc"
	"github.com/Alex4386/inaba/internal/selfupdate"
	"github.com/Alex4386/inaba/internal/version"
)

// restartDelay lets the answer to RestartHost get back before the host goes.
const restartDelay = 500 * time.Millisecond

// Restarts is closed when a restart has been asked for. Whoever runs the host then
// shuts it down and starts it again; a host nobody watches this for does not restart.
func (h *Host) Restarts() <-chan struct{} { return h.restart }

// RestartHost answers SystemService: it asks for a restart, which comes a moment later.
func (h *Host) RestartHost(ctx context.Context, _ *inabav1.RestartHostRequest) (*inabav1.RestartHostResponse, error) {
	if rpc.May(ctx, rpc.PermRestart) != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, "only the host's owner or an administrator can restart it")
	}
	h.scheduleRestart("restarting, as asked")
	return &inabav1.RestartHostResponse{}, nil
}

// scheduleRestart closes Restarts after restartDelay, once however often it is called.
func (h *Host) scheduleRestart(why string) {
	h.restarting.Do(func() {
		slog.Info(why)
		time.AfterFunc(restartDelay, func() { close(h.restart) })
	})
}

// UpgradeHost answers SystemService: it puts the newest build of a channel in place of
// the running binary, as inaba update does, then restarts onto it.
func (h *Host) UpgradeHost(ctx context.Context, req *inabav1.UpgradeHostRequest) (*inabav1.UpgradeHostResponse, error) {
	if rpc.May(ctx, rpc.PermRestart) != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, "only the host's owner or an administrator can update it")
	}
	now := version.String()
	channel := cmp.Or(req.Channel, selfupdate.ChannelOf(now))
	if !slices.Contains(selfupdate.Channels, channel) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Sprintf("no channel %q: it is one of %s", channel, strings.Join(selfupdate.Channels, ", ")))
	}
	// The gh sign-in is whoever runs the host's, so using it is asked for each time.
	if channel == selfupdate.Nightly && !req.UseGh {
		return nil, connect.NewError(connect.CodeFailedPrecondition, "a nightly build is fetched with the gh CLI, as the GitHub account signed in to it on the host; agree to that first")
	}
	// One at a time: two would race to replace the same file.
	if !h.upgrading.TryLock() {
		return nil, connect.NewError(connect.CodeAborted, "an update is already under way")
	}
	defer h.upgrading.Unlock()
	b, err := selfupdate.Latest(ctx, channel)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err.Error())
	}
	res := &inabav1.UpgradeHostResponse{From: now, To: now, Channel: channel}
	if b.Current(now) {
		return res, nil
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return nil, err
	}
	slog.Info("updating", "from", now, "channel", channel, "to", b.Version)
	said, err := selfupdate.Install(ctx, b, "inaba", exe)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err.Error())
	}
	res.To, res.Updated = strings.TrimPrefix(said, "inaba version "), true
	h.scheduleRestart("restarting onto " + res.To)
	return res, nil
}

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
