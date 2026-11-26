package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestChannelOf(t *testing.T) {
	for v, want := range map[string]string{
		"nightly-20261126-3eb4834": Nightly,
		"v1.2.0-rc.1":              PreRelease,
		"v1.2.0":                   Release,
		"3eb4834":                  Release,
		"dev":                      Release,
	} {
		if got := ChannelOf(v); got != want {
			t.Errorf("ChannelOf(%q) = %q, want %q", v, got, want)
		}
	}
}

func TestCurrent(t *testing.T) {
	nightly := &Build{Version: "nightly 3eb4834", Commit: "3eb4834d5e0801c51b07c5cdd998e169d5ba07fe"}
	if !nightly.Current("nightly-20261126-3eb4834") {
		t.Error("the nightly built from the same commit is not current")
	}
	for _, v := range []string{"nightly-20261126-1467df1", "3eb4834", "v1.0.0", "nightly-20261126-"} {
		if nightly.Current(v) {
			t.Errorf("%q is taken for the nightly of 3eb4834", v)
		}
	}
	rel := &Build{Version: "v1.0.0"}
	if !rel.Current("v1.0.0") || rel.Current("v1.0.1") {
		t.Error("a release is current only when the tag matches")
	}
}

func TestPickRelease(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 11, d, 0, 0, 0, 0, time.UTC) }
	list := []release{
		{Tag: "v1.1.0-rc.1", Prerelease: true, Published: day(20)},
		{Tag: "v1.2.0", Draft: true, Published: day(25)},
		{Tag: "v1.0.1", Published: day(10)},
		{Tag: "v1.0.0", Published: day(1)},
	}
	if r := pickRelease(list, false); r == nil || r.Tag != "v1.0.1" {
		t.Errorf("release channel picked %+v", r)
	}
	if r := pickRelease(list, true); r == nil || r.Tag != "v1.1.0-rc.1" {
		t.Errorf("pre-release channel picked %+v", r)
	}
	if r := pickRelease(list[:2], false); r != nil {
		t.Errorf("picked %q though there is no release", r.Tag)
	}
}

func TestPickArtifact(t *testing.T) {
	mk := func(branch string, expired bool, d int, sha string) artifact {
		a := artifact{Name: "inaba_nightly_linux_amd64", Expired: expired, Created: time.Date(2026, 11, d, 0, 0, 0, 0, time.UTC)}
		a.Run.Branch, a.Run.SHA = branch, sha
		return a
	}
	list := []artifact{mk("main", false, 20, "old"), mk("ci-tweak", false, 25, "pr"), mk("main", true, 24, "gone"), mk("main", false, 22, "new")}
	if a := pickArtifact(list, "inaba_nightly_linux_amd64"); a == nil || a.Run.SHA != "new" {
		t.Errorf("picked %+v", a)
	}
	if a := pickArtifact(list, "inaba_nightly_darwin_arm64"); a != nil {
		t.Errorf("picked %+v for another platform", a)
	}
}

func TestSumFor(t *testing.T) {
	h := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	list := []byte(h + "  inaba_v1.0.0_linux_amd64.tar.gz\n" + h[:63] + "0 *inaba_v1.0.0_windows_amd64.zip\n")
	if got := sumFor(list, "inaba_v1.0.0_linux_amd64.tar.gz"); got != h {
		t.Errorf("got %q", got)
	}
	if got := sumFor(list, "inaba_v1.0.0_windows_amd64.zip"); got != h[:63]+"0" {
		t.Errorf("binary-mode entry: got %q", got)
	}
	if got := sumFor(list, "inaba_v1.0.0_darwin_arm64.tar.gz"); got != "" {
		t.Errorf("unlisted file: got %q", got)
	}
}

// Install takes the program out of the archive, runs it, and puts it in place.
func TestInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in program is a shell script")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "inaba")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	b := &Build{Version: "v9.9.9", Archive: "inaba_v9.9.9_" + platform() + ".tar.gz"}
	b.fetch = func(_ context.Context, to string) error {
		f, err := os.Create(filepath.Join(to, b.Archive))
		if err != nil {
			return err
		}
		defer f.Close()
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		for name, body := range map[string]string{
			"inaba_v9.9.9_x/README.md":    "hi",
			"inaba_v9.9.9_x/inaba-server": "#!/bin/sh\necho wrong one\n",
			"inaba_v9.9.9_x/inaba":        "#!/bin/sh\necho inaba version v9.9.9\n",
		} {
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
				return err
			}
			if _, err := tw.Write([]byte(body)); err != nil {
				return err
			}
		}
		if err := tw.Close(); err != nil {
			return err
		}
		return gz.Close()
	}
	said, err := Install(context.Background(), b, "inaba", exe)
	if err != nil {
		t.Fatal(err)
	}
	if said != "inaba version v9.9.9" {
		t.Errorf("the new one said %q", said)
	}
	if got, _ := os.ReadFile(exe); string(got) != "#!/bin/sh\necho inaba version v9.9.9\n" {
		t.Errorf("%s is now %q", exe, got)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".inaba.new-*")); len(left) > 0 {
		t.Errorf("left behind: %v", left)
	}
}

// A program that does not run is not put in place.
func TestInstallKeepsOldOnBrokenBuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in program is a shell script")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "inaba")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	b := &Build{Version: "v9.9.9", Archive: "x.tar.gz"}
	b.fetch = func(_ context.Context, to string) error {
		f, _ := os.Create(filepath.Join(to, b.Archive))
		defer f.Close()
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		body := "#!/bin/sh\nexit 3\n"
		_ = tw.WriteHeader(&tar.Header{Name: "d/inaba", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
		_ = tw.Close()
		return gz.Close()
	}
	if _, err := Install(context.Background(), b, "inaba", exe); err == nil {
		t.Fatal("a program that fails --version was installed")
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Errorf("%s was changed to %q", exe, got)
	}
}
