package skillhub

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
)

const (
	maxSkillZipEntries    = 10000
	maxSkillUncompressed  = 500 * 1024 * 1024
	maxSkillManifestBytes = 64 * 1024
)

var (
	zipSegmentPattern   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	windowsReservedName = regexp.MustCompile(`^(?:con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\..*)?$`)
)

type zipFileInfo struct {
	Path      string
	Size      int64
	Language  string
	Truncated bool
	SHA256    string
}

// openSafeSkillZip validates the ZIP envelope and returns an opened reader.
// Refuses traversal, absolute paths, symlinks, encrypted entries, duplicate
// (case-folded) names and oversized archives.
func openSafeSkillZip(zipPath string) (*zip.ReadCloser, error) {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var total uint64
	entries := 0
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		if file.Flags&0x1 != 0 {
			reader.Close()
			return nil, errors.New("encrypted zip entries are not supported")
		}
		if file.Mode()&os.ModeSymlink != 0 {
			reader.Close()
			return nil, errors.New("symbolic links are not allowed")
		}
		if !isSafeRelativePath(file.Name) {
			reader.Close()
			return nil, fmt.Errorf("unsafe zip path %q", file.Name)
		}
		lower := strings.ToLower(file.Name)
		if _, dup := seen[lower]; dup {
			reader.Close()
			return nil, fmt.Errorf("duplicate zip path %q", file.Name)
		}
		seen[lower] = struct{}{}
		if ignoredSkillPath(file.Name) {
			continue
		}
		entries++
		if entries > maxSkillZipEntries {
			reader.Close()
			return nil, errors.New("too many zip entries")
		}
		total += file.UncompressedSize64
		if total > maxSkillUncompressed {
			reader.Close()
			return nil, errors.New("zip expands beyond the accepted size")
		}
	}
	return reader, nil
}

func ignoredSkillPath(name string) bool {
	lower := strings.ToLower(name)
	base := path.Base(name)
	return strings.HasPrefix(lower, "__macosx/") ||
		strings.HasPrefix(base, "._") ||
		base == ".ds_store"
}

// listSkillZip returns the file inventory without content hashes.
func listSkillZip(zipPath string) ([]zipFileInfo, error) {
	reader, err := openSafeSkillZip(zipPath)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	files := make([]zipFileInfo, 0, len(reader.File))
	for _, file := range reader.File {
		if file.FileInfo().IsDir() || ignoredSkillPath(file.Name) {
			continue
		}
		files = append(files, zipFileInfo{
			Path:      file.Name,
			Size:      int64(file.UncompressedSize64),
			Language:  languageFor(file.Name),
			Truncated: file.UncompressedSize64 > maxFileContentBytes,
		})
	}
	return files, nil
}

// inspectSkillZip validates SKILL.md and returns the inventory with hashes.
// Used when publishing so the file tree is queryable without re-reading S3.
func inspectSkillZip(zipPath string) ([]zipFileInfo, error) {
	reader, err := openSafeSkillZip(zipPath)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	files := make([]zipFileInfo, 0, len(reader.File))
	hasManifest := false
	for _, file := range reader.File {
		if file.FileInfo().IsDir() || ignoredSkillPath(file.Name) {
			continue
		}
		if file.Name == "SKILL.md" {
			hasManifest = true
			if file.UncompressedSize64 > maxSkillManifestBytes {
				return nil, errors.New("SKILL.md is too large")
			}
		}
		size := int64(file.UncompressedSize64)
		digest, err := hashZipEntry(file)
		if err != nil {
			return nil, err
		}
		files = append(files, zipFileInfo{
			Path:      file.Name,
			Size:      size,
			Language:  languageFor(file.Name),
			Truncated: file.UncompressedSize64 > maxFileContentBytes,
			SHA256:    digest,
		})
	}
	if !hasManifest {
		return nil, errors.New("SKILL.md must be at the package root")
	}
	return files, nil
}

func hashZipEntry(file *zip.File) (string, error) {
	rc, err := file.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	hasher := sha256.New()
	written, err := io.Copy(hasher, io.LimitReader(rc, maxSkillUncompressed+1))
	if err != nil {
		return "", err
	}
	if written > maxSkillUncompressed {
		return "", errors.New("zip entry expands beyond the accepted size")
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// computeFolderHash mirrors the desktop algorithm: sort "relPath:fileSha256"
// lines lexicographically, join with newlines and hash the result.
func computeFolderHash(files []zipFileInfo) string {
	lines := make([]string, 0, len(files))
	for _, file := range files {
		lines = append(lines, file.Path+":"+file.SHA256)
	}
	sort.Strings(lines)
	hasher := sha256.New()
	_, _ = io.WriteString(hasher, strings.Join(lines, "\n"))
	return hex.EncodeToString(hasher.Sum(nil))
}

// readSkillZipFile streams one file out of the archive, capping the returned
// content at 1 MiB and reporting truncation.
func readSkillZipFile(zipPath, target string) (content string, size int64, truncated bool, err error) {
	if !isSafeRelativePath(target) {
		return "", 0, false, errors.New("unsafe path")
	}
	reader, err := openSafeSkillZip(zipPath)
	if err != nil {
		return "", 0, false, err
	}
	defer reader.Close()
	for _, file := range reader.File {
		if file.FileInfo().IsDir() || file.Name != target {
			continue
		}
		size = int64(file.UncompressedSize64)
		rc, err := file.Open()
		if err != nil {
			return "", 0, false, err
		}
		defer rc.Close()
		raw, err := io.ReadAll(io.LimitReader(rc, maxFileContentBytes+1))
		if err != nil {
			return "", 0, false, err
		}
		if len(raw) > maxFileContentBytes {
			raw = raw[:maxFileContentBytes]
			truncated = true
		}
		return string(raw), size, truncated, nil
	}
	return "", 0, false, os.ErrNotExist
}

func isSafeRelativePath(p string) bool {
	if p == "" || len(p) > 512 || strings.Contains(p, "\\") || strings.ContainsRune(p, 0) {
		return false
	}
	if strings.HasPrefix(p, "/") {
		return false
	}
	if len(p) >= 2 && p[1] == ':' {
		return false
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		if !zipSegmentPattern.MatchString(segment) || windowsReservedName.MatchString(segment) {
			return false
		}
	}
	return true
}

func languageFor(name string) string {
	ext := strings.ToLower(path.Ext(name))
	switch ext {
	case ".md", ".markdown":
		return "markdown"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".toml":
		return "toml"
	case ".js", ".mjs", ".cjs", ".jsx":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".sh", ".bash", ".zsh":
		return "shell"
	case ".html", ".htm":
		return "html"
	case ".css", ".scss":
		return "css"
	case ".sql":
		return "sql"
	case ".xml":
		return "xml"
	case ".txt", ".log":
		return "text"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".pdf", ".zip", ".gz", ".woff", ".woff2", ".ttf":
		return "binary"
	default:
		return "text"
	}
}
