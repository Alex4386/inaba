// Package selfupdate replaces the running binary with a newer build from GitHub: a
// release or prerelease from the repository's releases, or the last nightly build,
// which is an Actions artifact and so is fetched with the gh CLI.
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// Repo is where builds come from.
const Repo = "Alex4386/inaba"

// Channels, in the order of how much they are trusted.
const (
	Release    = "release"
	PreRelease = "pre-release"
	Nightly    = "nightly"
)

// Channels are the names --channel takes.
var Channels = []string{Release, PreRelease, Nightly}

// ChannelOf is the channel a version came from: nightly builds say nightly-…, a tag
// with a dash (v1.2.0-rc.1) is a prerelease, and anything else follows releases.
func ChannelOf(version string) string {
	switch {
	case strings.HasPrefix(version, "nightly-"):
		return Nightly
	case strings.HasPrefix(version, "v") && strings.Contains(version, "-"):
		return PreRelease
	}
	return Release
}

// Build is one build a channel offers for this platform.
type Build struct {
	// Version is what the build will say with --version: the tag, or for a nightly the
	// commit it was built from, which is all a nightly's version is known by beforehand.
	Version string
	// Commit is the nightly's full commit; empty for releases.
	Commit string
	// Archive is the file's name, and fetch writes it into a folder.
	Archive string
	fetch   func(ctx context.Context, dir string) error
	sum     string // the archive's SHA-256, when the release lists one
}

// Current says whether the running version is this build.
func (b *Build) Current(version string) bool {
	if b.Commit != "" {
		// nightly-20261126-3eb4834: the last part is the commit.
		i := strings.LastIndexByte(version, '-')
		return strings.HasPrefix(version, "nightly-") && i > 0 && len(version)-i > 4 && strings.HasPrefix(b.Commit, version[i+1:])
	}
	return version == b.Version
}

func platform() string {
	return runtime.GOOS + "_" + runtime.GOARCH
}

func archiveExt() string {
	if runtime.GOOS == "windows" {
		return ".zip"
	}
	return ".tar.gz"
}

// Latest finds the newest build of the channel for this platform.
func Latest(ctx context.Context, channel string) (*Build, error) {
	switch channel {
	case Release, PreRelease:
		return latestRelease(ctx, channel == PreRelease)
	case Nightly:
		return latestNightly(ctx)
	}
	return nil, fmt.Errorf("no channel %q: it is one of %s", channel, strings.Join(Channels, ", "))
}

// release is the part of GitHub's release that matters here.
type release struct {
	Tag        string    `json:"tag_name"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Published  time.Time `json:"published_at"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// pickRelease is the newest release, prereleases too when pre is set. GitHub lists
// them by creation, which is not always the order they came out in.
func pickRelease(list []release, pre bool) *release {
	list = slices.DeleteFunc(slices.Clone(list), func(r release) bool { return r.Draft || r.Prerelease && !pre })
	if len(list) == 0 {
		return nil
	}
	r := slices.MaxFunc(list, func(a, b release) int { return a.Published.Compare(b.Published) })
	return &r
}

var client = &http.Client{Timeout: 5 * time.Minute}

func get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "inaba-update")
	if strings.HasPrefix(url, "https://api.github.com/") {
		req.Header.Set("Accept", "application/vnd.github+json")
		// Releases are public; a token only lifts the rate limit.
		if t := cmp.Or(os.Getenv("GH_TOKEN"), os.Getenv("GITHUB_TOKEN")); t != "" {
			req.Header.Set("Authorization", "Bearer "+t)
		}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("GitHub answered %s for %s", res.Status, url)
	}
	return res, nil
}

func latestRelease(ctx context.Context, pre bool) (*Build, error) {
	res, err := get(ctx, "https://api.github.com/repos/"+Repo+"/releases?per_page=50")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var list []release
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("reading the releases: %w", err)
	}
	r := pickRelease(list, pre)
	if r == nil {
		if !pre && pickRelease(list, true) != nil {
			return nil, errors.New("there is no release yet, only prereleases: try --channel=pre-release")
		}
		return nil, errors.New("there are no releases yet")
	}
	name := "inaba_" + r.Tag + "_" + platform() + archiveExt()
	var url, sums string
	for _, a := range r.Assets {
		switch a.Name {
		case name:
			url = a.URL
		case "SHA256SUMS":
			sums = a.URL
		}
	}
	if url == "" {
		return nil, fmt.Errorf("release %s has no build for %s/%s", r.Tag, runtime.GOOS, runtime.GOARCH)
	}
	b := &Build{Version: r.Tag, Archive: name}
	b.fetch = func(ctx context.Context, dir string) error {
		if sums == "" {
			return fmt.Errorf("release %s has no SHA256SUMS to check the download against", r.Tag)
		}
		list, err := fetchBytes(ctx, sums)
		if err != nil {
			return err
		}
		if b.sum = sumFor(list, name); b.sum == "" {
			return fmt.Errorf("SHA256SUMS of %s does not list %s", r.Tag, name)
		}
		data, err := fetchBytes(ctx, url)
		if err != nil {
			return err
		}
		if got := sha256.Sum256(data); hex.EncodeToString(got[:]) != b.sum {
			return fmt.Errorf("%s does not match its SHA256SUMS entry; nothing was changed", name)
		}
		return os.WriteFile(filepath.Join(dir, name), data, 0o600)
	}
	return b, nil
}

func fetchBytes(ctx context.Context, url string) ([]byte, error) {
	res, err := get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	// The archives are some tens of megabytes; this only stops a runaway download.
	return io.ReadAll(io.LimitReader(res.Body, 1<<30))
}

// sumFor finds name's hash in a sha256sum listing.
func sumFor(list []byte, name string) string {
	for _, line := range strings.Split(string(list), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

// artifact is the part of GitHub's Actions artifact that matters here.
type artifact struct {
	Name    string    `json:"name"`
	Expired bool      `json:"expired"`
	Created time.Time `json:"created_at"`
	Run     struct {
		ID     int64  `json:"id"`
		Branch string `json:"head_branch"`
		SHA    string `json:"head_sha"`
	} `json:"workflow_run"`
}

// pickArtifact is the newest artifact still there that was built from the default
// branch: the nightly workflow also builds pull requests that touch it.
func pickArtifact(list []artifact, name string) *artifact {
	list = slices.DeleteFunc(slices.Clone(list), func(a artifact) bool { return a.Name != name || a.Expired || a.Run.Branch != "main" })
	if len(list) == 0 {
		return nil
	}
	a := slices.MaxFunc(list, func(a, b artifact) int { return a.Created.Compare(b.Created) })
	return &a
}

func latestNightly(ctx context.Context) (*Build, error) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		return nil, errors.New("nightly builds are GitHub Actions artifacts, which only signed-in users can download: install the gh CLI (https://cli.github.com) and run gh auth login")
	}
	name := "inaba_nightly_" + platform()
	out, err := exec.CommandContext(ctx, gh, "api", "repos/"+Repo+"/actions/artifacts?per_page=100&name="+name).Output()
	if err != nil {
		return nil, ghError("listing the nightly builds", err)
	}
	var list struct {
		Artifacts []artifact `json:"artifacts"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("reading the nightly builds: %w", err)
	}
	a := pickArtifact(list.Artifacts, name)
	if a == nil {
		return nil, fmt.Errorf("there is no nightly build for %s/%s right now (they expire after 90 days)", runtime.GOOS, runtime.GOARCH)
	}
	b := &Build{Version: "nightly " + a.Run.SHA[:min(7, len(a.Run.SHA))], Commit: a.Run.SHA, Archive: name + archiveExt()}
	b.fetch = func(ctx context.Context, dir string) error {
		// gh unpacks the artifact's zip, which holds the archive the build made.
		cmd := exec.CommandContext(ctx, gh, "run", "download", fmt.Sprint(a.Run.ID), "-R", Repo, "-n", name, "-D", dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			return ghError("downloading the nightly build", fmt.Errorf("%w: %s", err, bytes.TrimSpace(out)))
		}
		return nil
	}
	return b, nil
}

func ghError(doing string, err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		err = errors.New(strings.TrimSpace(string(exit.Stderr)))
	}
	return fmt.Errorf("%s with gh: %w (is gh signed in? gh auth status)", doing, err)
}

// Install downloads the build, takes the program named name out of it, checks that it
// runs, and puts it in place of exe. On Windows, where a running program cannot be
// replaced, the old one is left beside it as name.old and removed next time.
func Install(ctx context.Context, b *Build, name, exe string) (said string, err error) {
	dir := filepath.Dir(exe)
	if runtime.GOOS == "windows" {
		name += ".exe"
		_ = os.Remove(exe + ".old")
	}
	tmp, err := os.MkdirTemp("", "inaba-update-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := b.fetch(ctx, tmp); err != nil {
		return "", err
	}
	archive, err := os.Open(filepath.Join(tmp, b.Archive))
	if err != nil {
		return "", fmt.Errorf("the download has no %s: %w", b.Archive, err)
	}
	defer archive.Close()
	// Beside the old one, so the swap is a rename on the same disk.
	next, err := os.CreateTemp(dir, "."+name+".new-*")
	if err != nil {
		return "", fmt.Errorf("cannot write next to %s (%w); run it as a user who can", exe, err)
	}
	defer os.Remove(next.Name())
	if strings.HasSuffix(b.Archive, ".zip") {
		err = fromZip(archive, name, next)
	} else {
		err = fromTarGz(archive, name, next)
	}
	if cerr := next.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if err := os.Chmod(next.Name(), 0o755); err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, next.Name(), "--version").Output()
	if err != nil {
		return "", fmt.Errorf("the new %s does not run here (%w); nothing was changed", name, err)
	}
	said = strings.TrimSpace(string(out))
	if runtime.GOOS == "windows" {
		if err := os.Rename(exe, exe+".old"); err != nil {
			return "", err
		}
		if err := os.Rename(next.Name(), exe); err != nil {
			_ = os.Rename(exe+".old", exe)
			return "", err
		}
		return said, nil
	}
	return said, os.Rename(next.Name(), exe)
}

// The archives hold one folder, inaba_<label>_<os>_<arch>, with the programs in it.
func inArchive(entry, name string) bool {
	entry = strings.TrimPrefix(entry, "./")
	return path.Base(entry) == name && strings.Count(strings.TrimSuffix(entry, "/"), "/") <= 1
}

func fromTarGz(r io.Reader, name string, w io.Writer) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	t := tar.NewReader(gz)
	for {
		h, err := t.Next()
		if err == io.EOF {
			return fmt.Errorf("the archive has no %s", name)
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg && inArchive(h.Name, name) {
			_, err = io.Copy(w, io.LimitReader(t, 1<<30))
			return err
		}
	}
}

func fromZip(f *os.File, name string, w io.Writer) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	z, err := zip.NewReader(f, fi.Size())
	if err != nil {
		return err
	}
	for _, e := range z.File {
		if !e.FileInfo().IsDir() && inArchive(e.Name, name) {
			rc, err := e.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			_, err = io.Copy(w, io.LimitReader(rc, 1<<30))
			return err
		}
	}
	return fmt.Errorf("the archive has no %s", name)
}
