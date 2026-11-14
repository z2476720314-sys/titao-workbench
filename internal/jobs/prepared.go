package jobs

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

const preparedIndex = "prepared-artwork.json"
const preparedAssets = "prepared-artwork-assets"

type PreparedArtwork struct {
	Path           string `json:"path"`
	SourceSHA256   string `json:"source_sha256"`
	PreparedSHA256 string `json:"prepared_sha256"`
	KeepWhole      bool   `json:"keep_whole,omitempty"`
}
type preparedFile struct {
	Version int                        `json:"version"`
	Entries map[string]PreparedArtwork `json:"entries"`
}
type PreparedCatalog struct {
	dir string
	preparedFile
}

func preparedSum(data []byte) string  { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func preparedName(path string) string { return strings.ToLower(filepath.Base(path)) }
func preparedRelative(name, sum string) string {
	return preparedAssets + "/" + preparedSum([]byte(name)) + "-" + sum + ".png"
}
func preparedValidSHA(sum string) bool {
	b, err := hex.DecodeString(sum)
	return err == nil && len(b) == sha256.Size && strings.ToLower(sum) == sum
}

// LoadPreparedCatalog reads a snapshot; only a missing index has an empty-index fallback.
func LoadPreparedCatalog(profileDir string) (*PreparedCatalog, error) {
	if strings.TrimSpace(profileDir) == "" {
		return nil, fmt.Errorf("profile目录为空")
	}
	dir, err := filepath.Abs(profileDir)
	if err != nil {
		return nil, err
	}
	c := &PreparedCatalog{dir: dir, preparedFile: preparedFile{1, map[string]PreparedArtwork{}}}
	r, err := os.OpenRoot(dir)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	defer r.Close()
	_, err = r.Lstat(preparedIndex)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	data, err := r.ReadFile(preparedIndex)
	if err != nil {
		return nil, err
	}
	var disk preparedFile
	if err := json.Unmarshal(data, &disk); err != nil {
		return nil, fmt.Errorf("重排副本索引损坏: %w", err)
	}
	if disk.Version != 1 || disk.Entries == nil {
		return nil, fmt.Errorf("重排副本索引版本或条目无效")
	}
	for name, entry := range disk.Entries {
		if name == "" || name == "." || name == ".." || name != preparedName(name) || strings.ContainsAny(name, `/\`) || !preparedValidSHA(entry.SourceSHA256) || !preparedValidSHA(entry.PreparedSHA256) || entry.Path != preparedRelative(name, entry.PreparedSHA256) {
			return nil, fmt.Errorf("重排副本索引条目无效: %s", name)
		}
		st, err := r.Stat(entry.Path)
		if err != nil {
			return nil, fmt.Errorf("重排副本资产不可用: %w", err)
		}
		if !st.Mode().IsRegular() {
			return nil, fmt.Errorf("重排副本资产不是普通文件")
		}
	}
	c.preparedFile = disk
	return c, nil
}

// Resolve hashes only registered names; a changed source needs a new explicit approval.
func (c *PreparedCatalog) Resolve(sourcePath string) (*PreparedArtwork, error) {
	entry, ok := c.Entries[preparedName(sourcePath)]
	if !ok {
		return nil, nil
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, err
	}
	if preparedSum(data) != entry.SourceSHA256 {
		return nil, nil
	}
	entry.Path = filepath.Join(c.dir, filepath.FromSlash(entry.Path))
	if err := ValidatePreparedInput(c.dir, sourcePath, entry.SourceSHA256, entry.Path, entry.PreparedSHA256); err != nil {
		return nil, err
	}
	return &entry, nil
}

// ValidatePreparedInput checks captured files without consulting a newer catalog.
func ValidatePreparedInput(profileDir, originalPath, sourceSHA, preparedPath, preparedSHA string) error {
	if strings.TrimSpace(profileDir) == "" {
		return fmt.Errorf("profile目录为空")
	}
	sourceSHA, preparedSHA = strings.ToLower(sourceSHA), strings.ToLower(preparedSHA)
	if !preparedValidSHA(sourceSHA) || !preparedValidSHA(preparedSHA) {
		return fmt.Errorf("重排副本快照SHA256无效")
	}
	data, err := os.ReadFile(originalPath)
	if err != nil {
		return err
	}
	if preparedSum(data) != sourceSHA {
		return fmt.Errorf("原图已变化，不能复用已捕获的重排副本")
	}
	path, err := filepath.Abs(preparedPath)
	if err != nil {
		return err
	}
	dir, err := filepath.Abs(profileDir)
	if err != nil {
		return err
	}
	relative := preparedRelative(preparedName(originalPath), preparedSHA)
	if !strings.EqualFold(filepath.Clean(path), filepath.Join(dir, filepath.FromSlash(relative))) {
		return fmt.Errorf("重排副本路径不属于profile资产目录")
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer r.Close()
	data, err = r.ReadFile(relative)
	if err != nil {
		return err
	}
	if preparedSum(data) != preparedSHA {
		return fmt.Errorf("重排副本PNG已被改动")
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		return fmt.Errorf("重排副本不是完整PNG: %w", err)
	}
	return nil
}

// RegisterPreparedArtwork keeps historical assets and atomically publishes the name/SHA binding.
func RegisterPreparedArtwork(profileDir, originalPath, approvedPNG, expectedOriginalSHA string, keepWhole ...bool) (*PreparedArtwork, error) {
	if strings.TrimSpace(profileDir) == "" {
		return nil, fmt.Errorf("profile目录为空")
	}
	source, err := os.ReadFile(originalPath)
	if err != nil {
		return nil, err
	}
	sourceSHA := preparedSum(source)
	if expectedOriginalSHA != "" && sourceSHA != strings.ToLower(expectedOriginalSHA) {
		return nil, fmt.Errorf("原图SHA256与验收快照不一致")
	}
	data, err := os.ReadFile(approvedPNG)
	if err != nil {
		return nil, err
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		return nil, fmt.Errorf("验收副本不是完整PNG: %w", err)
	}
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(profileDir)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	lock, err := r.OpenFile("prepared-artwork.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(lock.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped); err != nil {
		return nil, fmt.Errorf("重排副本索引正在导入: %w", err)
	}
	defer windows.UnlockFileEx(windows.Handle(lock.Fd()), 0, 1, 0, &overlapped)
	c, err := LoadPreparedCatalog(profileDir)
	if err != nil {
		return nil, err
	}
	name, sum := preparedName(originalPath), preparedSum(data)
	relative := preparedRelative(name, sum)
	if err := r.MkdirAll(preparedAssets, 0700); err != nil {
		return nil, err
	}
	existing, err := r.ReadFile(relative)
	if err == nil {
		if !bytes.Equal(existing, data) {
			return nil, fmt.Errorf("同名不可变重排副本已损坏，拒绝覆盖")
		}
	} else if os.IsNotExist(err) {
		if err := writePreparedAtomic(r, relative, data); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	if err := r.Chmod(relative, 0444); err != nil {
		return nil, err
	}
	entry := PreparedArtwork{Path: relative, SourceSHA256: sourceSHA, PreparedSHA256: sum}
	if len(keepWhole) > 0 {
		entry.KeepWhole = keepWhole[0]
	}
	absolute := filepath.Join(c.dir, filepath.FromSlash(relative))
	if err := ValidatePreparedInput(c.dir, originalPath, sourceSHA, absolute, sum); err != nil {
		return nil, err
	}
	c.Entries[name] = entry
	index, err := json.MarshalIndent(c.preparedFile, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writePreparedAtomic(r, preparedIndex, index); err != nil {
		return nil, err
	}
	entry.Path = absolute
	return &entry, nil
}

func writePreparedAtomic(r *os.Root, path string, data []byte) error {
	tmp := path + "." + rand.Text() + ".tmp"
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer r.Remove(tmp)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return r.Rename(tmp, path)
}
