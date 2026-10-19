package plugin

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

const (
	maxPluginZipEntries    = 2048
	maxPluginUncompressed  = 256 * 1024 * 1024
	maxPluginManifestBytes = 256 * 1024
)

var (
	ghostIDPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	zipSegmentPattern   = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,63}$`)
	semverPattern       = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	windowsReservedName = regexp.MustCompile(`^(?:con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\..*)?$`)
)

type pluginPackage struct {
	ID            string
	Name          string
	Version       string
	Author        string
	Description   string
	SchemaVersion int
	Manifest      json.RawMessage
}

// readPluginPackage validates the ZIP envelope, locates ghost.json at the
// archive root and returns a minimally normalized manifest. namespace is the
// server-owned organization slug (empty for public/personal packages).
func readPluginPackage(path, namespace string) (*pluginPackage, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	var manifestFile *zip.File
	var total uint64
	entries := 0
	seen := map[string]struct{}{}
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		if file.Flags&0x1 != 0 {
			return nil, errors.New("encrypted zip entries are not supported")
		}
		if file.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("symbolic links are not allowed")
		}
		if !isSafeRelativePath(file.Name) {
			return nil, fmt.Errorf("unsafe zip path %q", file.Name)
		}
		lower := strings.ToLower(file.Name)
		if _, dup := seen[lower]; dup {
			return nil, fmt.Errorf("duplicate zip path %q", file.Name)
		}
		seen[lower] = struct{}{}
		if strings.HasPrefix(lower, "__macosx/") {
			continue
		}
		entries++
		if entries > maxPluginZipEntries {
			return nil, errors.New("too many zip entries")
		}
		total += file.UncompressedSize64
		if total > maxPluginUncompressed {
			return nil, errors.New("zip expands beyond the accepted size")
		}
		if file.Name == "ghost.json" {
			manifestFile = file
		}
	}
	if manifestFile == nil {
		return nil, errors.New("ghost.json must be at the package root")
	}
	if manifestFile.UncompressedSize64 > maxPluginManifestBytes {
		return nil, errors.New("ghost.json is too large")
	}
	rc, err := manifestFile.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, maxPluginManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxPluginManifestBytes {
		return nil, errors.New("ghost.json is too large")
	}

	decoded := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("parse ghost.json: %w", err)
	}
	pkg := &pluginPackage{}
	if err := readInt(decoded, "schemaVersion", &pkg.SchemaVersion); err != nil {
		return nil, err
	}
	if pkg.SchemaVersion != 2 && pkg.SchemaVersion != 3 {
		return nil, errors.New("schemaVersion must be 2 or 3")
	}
	if err := readString(decoded, "id", &pkg.ID, true); err != nil {
		return nil, err
	}
	if !ghostIDPattern.MatchString(pkg.ID) || windowsReservedName.MatchString(pkg.ID) {
		return nil, errors.New("invalid ghost id")
	}
	if err := readString(decoded, "name", &pkg.Name, true); err != nil {
		return nil, err
	}
	if len([]rune(pkg.Name)) > 64 {
		return nil, errors.New("name is too long")
	}
	if err := readString(decoded, "version", &pkg.Version, true); err != nil {
		return nil, err
	}
	if len(pkg.Version) > 32 {
		return nil, errors.New("version is too long")
	}
	if err := readString(decoded, "author", &pkg.Author, true); err != nil {
		return nil, err
	}
	if len([]rune(pkg.Author)) > 64 {
		return nil, errors.New("author is too long")
	}
	if rawDescription, ok := decoded["description"]; ok {
		if err := json.Unmarshal(rawDescription, &pkg.Description); err != nil {
			return nil, errors.New("description must be a string")
		}
		if pkg.Description == "" || len([]rune(pkg.Description)) > 300 {
			return nil, errors.New("description must be 1-300 characters")
		}
	}
	if rawWhen, ok := decoded["whenToUse"]; ok {
		var when string
		if err := json.Unmarshal(rawWhen, &when); err != nil || when == "" || len([]rune(when)) > 300 {
			return nil, errors.New("whenToUse must be 1-300 characters")
		}
	}
	var entry string
	if err := readString(decoded, "entry", &entry, true); err != nil {
		return nil, err
	}
	if !isSafeRelativePath(entry) {
		return nil, errors.New("entry must be a safe relative path")
	}
	if _, declared := decoded["namespace"]; declared {
		return nil, errors.New("ghost.json must not declare namespace")
	}
	if pkg.SchemaVersion == 3 {
		var minVersion string
		if err := readString(decoded, "minCindyVersion", &minVersion, true); err != nil {
			return nil, err
		}
		if !semverPattern.MatchString(minVersion) {
			return nil, errors.New("minCindyVersion must be a valid semantic version")
		}
		if _, hasSlots := decoded["slots"]; hasSlots {
			return nil, errors.New("schemaVersion 3 must not declare slots")
		}
	} else if rawSlots, ok := decoded["slots"]; ok {
		var slots []json.RawMessage
		if err := json.Unmarshal(rawSlots, &slots); err != nil {
			return nil, errors.New("slots must be an array")
		}
	}
	if rawIcon, ok := decoded["icon"]; ok {
		var icon string
		if err := json.Unmarshal(rawIcon, &icon); err != nil || !isSafeRelativePath(icon) {
			return nil, errors.New("icon must be a safe relative path")
		}
	}

	if namespace != "" {
		encoded, err := json.Marshal(namespace)
		if err != nil {
			return nil, err
		}
		decoded["namespace"] = encoded
	} else {
		delete(decoded, "namespace")
	}
	normalized, err := json.Marshal(decoded)
	if err != nil {
		return nil, err
	}
	pkg.Manifest = normalized
	return pkg, nil
}

func readString(raw map[string]json.RawMessage, key string, target *string, required bool) error {
	value, ok := raw[key]
	if !ok {
		if required {
			return fmt.Errorf("%s is required", key)
		}
		return nil
	}
	if err := json.Unmarshal(value, target); err != nil {
		return fmt.Errorf("%s must be a string", key)
	}
	if required && *target == "" {
		return fmt.Errorf("%s must not be empty", key)
	}
	return nil
}

func readInt(raw map[string]json.RawMessage, key string, target *int) error {
	value, ok := raw[key]
	if !ok {
		return fmt.Errorf("%s is required", key)
	}
	if err := json.Unmarshal(value, target); err != nil {
		return fmt.Errorf("%s must be an integer", key)
	}
	return nil
}

// isSafeRelativePath enforces the client-side safe relative path rules:
// forward-slash segments, no traversal, no drive letters or backslashes.
func isSafeRelativePath(path string) bool {
	if path == "" || len(path) > 256 || strings.Contains(path, "\\") || strings.ContainsRune(path, 0) {
		return false
	}
	if strings.HasPrefix(path, "/") {
		return false
	}
	if len(path) >= 2 && path[1] == ':' {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		if !zipSegmentPattern.MatchString(segment) || windowsReservedName.MatchString(segment) {
			return false
		}
	}
	return true
}
