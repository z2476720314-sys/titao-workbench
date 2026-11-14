package jobs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func preparedFixture(t *testing.T) (profile, source, approved string, original []byte) {
	t.Helper()
	dir := t.TempDir()
	profile, source, approved = filepath.Join(dir, "profile"), filepath.Join(dir, "source", "item.png"), filepath.Join(dir, "approved.png")
	if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
		t.Fatal(err)
	}
	for i, path := range []string{source, approved} {
		im := image.NewNRGBA(image.Rect(0, 0, 3+i, 4))
		im.SetNRGBA(1, 1, color.NRGBA{R: uint8(80 + i), A: 255})
		var b bytes.Buffer
		if err := png.Encode(&b, im); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	original, _ = os.ReadFile(source)
	t.Cleanup(func() {
		filepath.WalkDir(profile, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && d.Type()&os.ModeSymlink == 0 {
				_ = os.Chmod(path, 0600)
			}
			return nil
		})
	})
	return
}

func fixtureSHA(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func TestPreparedArtworkSnapshotsReuseByNameAndSHAWithoutChangingSource(t *testing.T) {
	profile, source, approved, original := preparedFixture(t)
	catalog, err := LoadPreparedCatalog(profile)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := catalog.Resolve(filepath.Join(profile, "unregistered-missing.png")); err != nil || got != nil {
		t.Fatal("an unregistered name must not access or hash its source")
	}
	ref, err := RegisterPreparedArtwork(profile, source, approved, fixtureSHA(original))
	if err != nil {
		t.Fatal(err)
	}
	if ref == nil || ref.Path == approved || filepath.Dir(ref.Path) != filepath.Join(profile, "prepared-artwork-assets") {
		t.Fatal("approved artwork was not copied into the profile assets")
	}
	if err := ValidatePreparedInput(profile, source, ref.SourceSHA256, ref.Path, ref.PreparedSHA256); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(ref.Path)
	if _, err := RegisterPreparedArtwork(profile, source, approved, ""); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(ref.Path)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("identical registration rewrote an immutable asset")
	}
	catalog, err = LoadPreparedCatalog(profile)
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(t.TempDir(), filepath.Base(source))
	if err := os.WriteFile(moved, original, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := catalog.Resolve(moved)
	if err != nil || got == nil || *got != *ref {
		t.Fatalf("same named source snapshot did not resolve across directories: %+v %v", got, err)
	}
	if current, _ := os.ReadFile(source); !bytes.Equal(current, original) {
		t.Fatal("registration changed original image bytes")
	}
	if err := os.WriteFile(source, append(original, 'x'), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := catalog.Resolve(source); err != nil || got != nil {
		t.Fatal("a changed original reused stale prepared artwork")
	}
	if err := ValidatePreparedInput(profile, source, ref.SourceSHA256, ref.Path, ref.PreparedSHA256); err == nil {
		t.Fatal("captured task ignored a changed original")
	}
}

func TestPreparedArtworkDifferentNamesNeverShareEffectiveAssetPath(t *testing.T) {
	profile, source, approved, original := preparedFixture(t)
	a, err := RegisterPreparedArtwork(profile, source, approved, "")
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(filepath.Dir(source), "other-product.png")
	if err := os.WriteFile(other, original, 0600); err != nil {
		t.Fatal(err)
	}
	b, err := RegisterPreparedArtwork(profile, other, approved, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Path == b.Path || a.SourceSHA256 != b.SourceSHA256 || a.PreparedSHA256 != b.PreparedSHA256 {
		t.Fatal("different product names were merged by identical source or prepared bytes")
	}
}

func TestPreparedArtworkRejectsChangedAssetAndInvalidPNG(t *testing.T) {
	profile, source, approved, _ := preparedFixture(t)
	ref, err := RegisterPreparedArtwork(profile, source, approved, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ref.Path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ref.Path, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadPreparedCatalog(profile)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := catalog.Resolve(source); err == nil || got != nil {
		t.Fatal("tampered prepared PNG was accepted")
	}
	if err := ValidatePreparedInput(profile, source, ref.SourceSHA256, ref.Path, ref.PreparedSHA256); err == nil {
		t.Fatal("captured task ignored a changed asset")
	}
	if err := os.WriteFile(approved, []byte("not a png"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := RegisterPreparedArtwork(profile, source, approved, ""); err == nil {
		t.Fatal("non-PNG approved artwork was accepted")
	}
}

func TestPreparedArtworkRejectsCorruptIndexAndEscapingPaths(t *testing.T) {
	for _, mode := range []string{"json", "version", "path", "absolute", "sha"} {
		t.Run(mode, func(t *testing.T) {
			profile, source, approved, _ := preparedFixture(t)
			if _, err := RegisterPreparedArtwork(profile, source, approved, ""); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(profile, "prepared-artwork.json")
			data, _ := os.ReadFile(path)
			var index map[string]any
			if err := json.Unmarshal(data, &index); err != nil {
				t.Fatal(err)
			}
			record := index["entries"].(map[string]any)[strings.ToLower(filepath.Base(source))].(map[string]any)
			switch mode {
			case "version":
				index["version"] = 2
			case "path":
				record["path"] = "../outside.png"
			case "absolute":
				record["path"] = approved
			case "sha":
				record["source_sha256"] = "bad"
			}
			data, _ = json.Marshal(index)
			if mode == "json" {
				data = []byte("{broken")
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if catalog, err := LoadPreparedCatalog(profile); err == nil || catalog != nil {
				t.Fatal("corrupt or escaping index became an empty usable catalog")
			}
		})
	}
}

func TestPreparedArtworkAtomicFailurePreservesPreviousIndex(t *testing.T) {
	profile, source, approved, original := preparedFixture(t)
	if _, err := RegisterPreparedArtwork(profile, source, approved, ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(profile, "prepared-artwork.json")
	before, _ := os.ReadFile(path)
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if handle != windows.InvalidHandle {
			_ = windows.CloseHandle(handle)
		}
	}()
	other := filepath.Join(filepath.Dir(source), "second.png")
	if err := os.WriteFile(other, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := RegisterPreparedArtwork(profile, other, approved, ""); err == nil {
		t.Fatal("atomic index write failure was hidden")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("failed import replaced the previous index")
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	handle = windows.InvalidHandle
	if _, err := RegisterPreparedArtwork(profile, other, approved, ""); err != nil {
		t.Fatal("OS import lock was not released after failure: ", err)
	}
}

func TestPreparedArtworkExpectedSourceSHAAndAssetSymlinkBoundary(t *testing.T) {
	profile, source, approved, _ := preparedFixture(t)
	if _, err := RegisterPreparedArtwork(profile, source, approved, strings.Repeat("0", 64)); err == nil {
		t.Fatal("stale approval source SHA was accepted")
	}
	ref, err := RegisterPreparedArtwork(profile, source, approved, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ref.Path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ref.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(approved, ref.Path); err != nil {
		t.Skip("symlink creation unavailable: ", err)
	}
	if _, err := LoadPreparedCatalog(profile); err == nil {
		t.Fatal("asset symlink escaped profile root")
	}
	if err := ValidatePreparedInput(profile, source, ref.SourceSHA256, ref.Path, ref.PreparedSHA256); err == nil {
		t.Fatal("captured task followed an escaping asset symlink")
	}
}

func TestPreparedArtworkCapturedHistorySurvivesNewApproval(t *testing.T) {
	profile, source, approved, _ := preparedFixture(t)
	old, err := RegisterPreparedArtwork(profile, source, approved, "")
	if err != nil {
		t.Fatal(err)
	}
	im := image.NewNRGBA(image.Rect(0, 0, 8, 9))
	im.SetNRGBA(2, 3, color.NRGBA{B: 180, A: 255})
	var data bytes.Buffer
	if err := png.Encode(&data, im); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(approved, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	current, err := RegisterPreparedArtwork(profile, source, approved, old.SourceSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if current.Path == old.Path {
		t.Fatal("new approved bytes replaced a historical asset")
	}
	if err := ValidatePreparedInput(profile, source, old.SourceSHA256, old.Path, old.PreparedSHA256); err != nil {
		t.Fatal("captured historical input was rewritten: ", err)
	}
	catalog, err := LoadPreparedCatalog(profile)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := catalog.Resolve(source)
	if err != nil || resolved == nil || resolved.Path != current.Path {
		t.Fatal("new planning did not select the latest explicit approval")
	}
}

func TestPreparedArtworkRejectsDanglingIndexInsteadOfEmptyFallback(t *testing.T) {
	profile, source, approved, _ := preparedFixture(t)
	if _, err := RegisterPreparedArtwork(profile, source, approved, ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(profile, preparedIndex)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing-index.json", path); err != nil {
		t.Skip("symlink creation unavailable: ", err)
	}
	if catalog, err := LoadPreparedCatalog(profile); err == nil || catalog != nil {
		t.Fatal("an existing dangling index was silently treated as missing")
	}
}

func TestPreparedArtworkOSLockIgnoresStaleFileAndRejectsLiveOwner(t *testing.T) {
	profile, source, approved, _ := preparedFixture(t)
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(profile, "prepared-artwork.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped); err != nil {
		t.Fatal(err)
	}
	_, importErr := RegisterPreparedArtwork(profile, source, approved, "")
	if err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped); err != nil {
		t.Fatal(err)
	}
	if importErr == nil {
		t.Fatal("an active OS lock owner was ignored")
	}
	if _, err := RegisterPreparedArtwork(profile, source, approved, ""); err != nil {
		t.Fatal("the remaining empty lock file was mistaken for an active owner: ", err)
	}
}

func TestPreparedArtworkCapturedInputRejectsAnotherProfile(t *testing.T) {
	profile, source, approved, _ := preparedFixture(t)
	first, err := RegisterPreparedArtwork(profile, source, approved, "")
	if err != nil {
		t.Fatal(err)
	}
	otherProfile := filepath.Join(t.TempDir(), "other-profile")
	other, err := RegisterPreparedArtwork(otherProfile, source, approved, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(other.Path, 0600) })
	if err := ValidatePreparedInput(profile, source, first.SourceSHA256, other.Path, first.PreparedSHA256); err == nil {
		t.Fatal("captured path from a different profile was trusted solely by its assets directory name")
	}
}

func TestPreparedArtworkStaleTemporaryFilesDoNotBlockImport(t *testing.T) {
	for _, asset := range []bool{false, true} {
		t.Run(fmt.Sprint(asset), func(t *testing.T) {
			profile, source, approved, _ := preparedFixture(t)
			if err := os.MkdirAll(filepath.Join(profile, preparedAssets), 0700); err != nil {
				t.Fatal(err)
			}
			stale := filepath.Join(profile, preparedIndex+".tmp")
			if asset {
				data, _ := os.ReadFile(approved)
				stale = filepath.Join(profile, filepath.FromSlash(preparedRelative(preparedName(source), fixtureSHA(data)))) + ".tmp"
			}
			if err := os.WriteFile(stale, []byte("interrupted old write"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := RegisterPreparedArtwork(profile, source, approved, ""); err != nil {
				t.Fatal("an interrupted temporary file blocked a new atomic import: ", err)
			}
		})
	}
}

func TestPreparedArtworkCapturedInputAcceptsWindowsDriveCase(t *testing.T) {
	profile, source, approved, original := preparedFixture(t)
	ref, err := RegisterPreparedArtwork(profile, source, approved, fixtureSHA(original))
	if err != nil {
		t.Fatal(err)
	}
	otherDriveCase := strings.ToLower(profile[:1]) + profile[1:]
	if otherDriveCase == profile {
		otherDriveCase = strings.ToUpper(profile[:1]) + profile[1:]
	}
	if err = ValidatePreparedInput(otherDriveCase, source, ref.SourceSHA256, ref.Path, ref.PreparedSHA256); err != nil {
		t.Fatalf("same Windows profile rejected for drive-letter case: %v", err)
	}
}
