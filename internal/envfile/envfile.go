// Package envfile reads and writes dotenv files the way Docker Compose does:
// last definition wins, CHANGE_ME and ${...} values are placeholders, and
// files holding secrets are written atomically with owner-only permissions.
package envfile

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func WritePrivate(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return err
		}
		if retryErr := os.Rename(tmpPath, path); retryErr != nil {
			return retryErr
		}
	}
	return os.Chmod(path, 0600)
}

func EnsurePrivateMode(path string) {
	_ = os.Chmod(path, 0600)
}

// supersededEnvComment marks an earlier duplicate definition the launcher has
// retired. The line is commented rather than deleted so a user who added an
// override can see where it went, and why it was not the one taking effect.
const SupersededComment = "# [ligand-x] superseded — the definition below is the one Docker Compose reads: "

// envKeyOnLine returns the key a line defines, or "" for blanks, comments and
// non-assignments. It mirrors parseEnvFile and compose's dotenv parser: the key
// is whatever precedes the first '=', trimmed, so `KEY = value` defines KEY
// exactly as `KEY=value` does.
func KeyOnLine(line string) string {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return ""
	}
	i := strings.Index(t, "=")
	if i <= 0 {
		return ""
	}
	return strings.TrimSpace(t[:i])
}

// duplicateEnvKeys returns, sorted, every key with more than one live
// definition. A duplicate is invisible in an editor but decisive at runtime —
// compose takes the last one — so an override inserted above the original
// silently does nothing. Worth naming in the log for every key, not just the
// resource limits: it applies equally to VERSION and POSTGRES_PASSWORD.
func DuplicateKeys(content string) []string {
	counts := map[string]int{}
	for _, line := range strings.Split(content, "\n") {
		if key := KeyOnLine(line); key != "" {
			counts[key]++
		}
	}
	var dups []string
	for k, n := range counts {
		if n > 1 {
			dups = append(dups, k)
		}
	}
	slices.Sort(dups)
	return dups
}

// parseEnvFile parses KEY=VALUE lines (ignoring comments/blanks) into a map.
func Parse(content string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.Index(line, "="); i > 0 {
			out[strings.TrimSpace(line[:i])] = strings.TrimSpace(line[i+1:])
		}
	}
	return out
}

// isEnvPlaceholder reports whether a value still needs generating: empty, a
// template CHANGE_ME marker, or an unresolved compose/env substitution.
func IsPlaceholder(v string) bool {
	return v == "" || strings.Contains(v, "CHANGE_ME") || strings.Contains(v, "${")
}

func IsPinnedVersion(version string) bool {
	v := strings.TrimSpace(version)
	return v != "" && !IsPlaceholder(v) && !strings.EqualFold(v, "latest")
}
