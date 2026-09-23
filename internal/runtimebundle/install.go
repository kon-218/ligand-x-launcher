package runtimebundle

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (p Policy) ApprovedDownloadURL(sourceURL string) (*url.URL, error) {
	parsed, err := url.Parse(sourceURL)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == "" || parsed.Scheme == "file" {
		if !p.AllowLocalSources {
			return nil, fmt.Errorf("local runtime bundle URLs are disabled in public builds")
		}
		return parsed, nil
	}
	if parsed.Scheme != "https" || parsed.User != nil || parsed.Hostname() == "" {
		return nil, fmt.Errorf("runtime bundle URL must use HTTPS without embedded credentials")
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return nil, fmt.Errorf("runtime bundle URL uses an unapproved port")
	}
	host := strings.ToLower(parsed.Hostname())
	approved := host == "github.com" || host == "api.github.com" ||
		host == "objects.githubusercontent.com" || host == "release-assets.githubusercontent.com" ||
		strings.HasSuffix(host, ".githubusercontent.com")
	if !approved {
		return nil, fmt.Errorf("runtime bundle host is not approved: %s", host)
	}
	return parsed, nil
}

func (p Policy) DownloadLimited(sourceURL, dest string, maxBytes int64) error {
	parsed, err := p.ApprovedDownloadURL(sourceURL)
	if err != nil {
		return err
	}
	var reader io.ReadCloser
	if parsed.Scheme == "file" || parsed.Scheme == "" {
		path := parsed.Path
		if parsed.Scheme == "" {
			path = sourceURL
		}
		reader, err = os.Open(path)
		if err != nil {
			return err
		}
	} else {
		client := &http.Client{
			Timeout: 20 * time.Minute,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many runtime bundle redirects")
				}
				_, err := p.ApprovedDownloadURL(req.URL.String())
				return err
			},
		}
		resp, requestErr := client.Get(sourceURL)
		if requestErr != nil {
			return requestErr
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return fmt.Errorf("HTTP %d from %s", resp.StatusCode, sourceURL)
		}
		if resp.ContentLength > maxBytes {
			resp.Body.Close()
			return fmt.Errorf("download exceeds maximum size")
		}
		reader = resp.Body
	}
	defer reader.Close()

	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := out.Name()
	defer os.Remove(tmpPath)
	if err := out.Chmod(0600); err != nil {
		out.Close()
		return err
	}
	written, copyErr := io.Copy(out, io.LimitReader(reader, maxBytes+1))
	if copyErr == nil && written > maxBytes {
		copyErr = fmt.Errorf("download exceeds maximum size")
	}
	if copyErr == nil {
		copyErr = out.Sync()
	}
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		if removeErr := os.Remove(dest); removeErr != nil && !os.IsNotExist(removeErr) {
			return err
		}
		if retryErr := os.Rename(tmpPath, dest); retryErr != nil {
			return retryErr
		}
	}
	return nil
}

func rejectSymlinkPath(baseDir, target string) error {
	baseAbs, err := filepath.Abs(baseDir)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(baseAbs, targetAbs)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("runtime bundle target escapes destination")
	}
	current := baseAbs
	for _, component := range strings.Split(relative, string(os.PathSeparator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("runtime bundle target traverses a symbolic link: %s", current)
		}
	}
	return nil
}

func Extract(zipPath, destDir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	if len(zr.File) > MaxFiles {
		return fmt.Errorf("runtime bundle contains too many entries")
	}
	var expandedBytes uint64
	required := map[string]bool{
		"docker-compose.yml":       false,
		".env.production.template": false,
	}

	for _, f := range zr.File {
		if f.UncompressedSize64 > MaxExpandedBytes-expandedBytes {
			return fmt.Errorf("runtime bundle exceeds expanded-size limit")
		}
		expandedBytes += f.UncompressedSize64
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("runtime bundle contains a symbolic link: %s", f.Name)
		}
		name := normalizedEntryName(f.Name)
		if name == "" || !EntryAllowed(name) {
			continue
		}
		target := filepath.Join(destDir, filepath.FromSlash(name))
		cleanDest, _ := filepath.Abs(destDir)
		cleanTarget, _ := filepath.Abs(target)
		if cleanTarget != cleanDest && !strings.HasPrefix(cleanTarget, cleanDest+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path in runtime bundle: %s", f.Name)
		}
		if err := rejectSymlinkPath(destDir, target); err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		// Self-heal stale installs: an earlier run with a missing bundle source
		// could leave a directory where Docker auto-created the bind-mount source
		// (e.g. docker/nginx/ligandx.conf as a dir). We're about to write a file
		// here, so remove a colliding directory first or os.OpenFile will fail
		// with "is a directory".
		if info, statErr := os.Stat(target); statErr == nil && info.IsDir() {
			if err := os.RemoveAll(target); err != nil {
				return err
			}
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
		if err != nil {
			rc.Close()
			return err
		}
		entrySize := int64(f.UncompressedSize64) // #nosec G115 -- bounded by runtimeBundleMaxExpandedBytes above.
		written, copyErr := io.Copy(out, io.LimitReader(rc, entrySize+1))
		if copyErr == nil && written != entrySize {
			copyErr = fmt.Errorf("runtime bundle entry size mismatch: %s", f.Name)
		}
		closeErr := out.Close()
		rc.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if _, needed := required[name]; needed {
			required[name] = true
		}
	}
	for name, present := range required {
		if !present {
			return fmt.Errorf("runtime bundle is missing required file: %s", name)
		}
	}
	return nil
}

// activateRuntimeStage copies a fully verified/extracted bundle into the
// managed runtime one file at a time, retaining originals until the caller
// commits. It never touches .env.production, Docker volumes, or user results.
func ActivateStage(stageDir, destDir, backupDir string) (func(), error) {
	type activatedFile struct {
		target  string
		backup  string
		existed bool
	}
	activated := []activatedFile{}
	rollback := func() {
		for index := len(activated) - 1; index >= 0; index-- {
			item := activated[index]
			if item.existed {
				_ = copyFileAtomic(item.backup, item.target, 0644)
			} else {
				_ = os.Remove(item.target)
			}
		}
	}
	err := filepath.Walk(stageDir, func(source string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(stageDir, source)
		if err != nil {
			return err
		}
		target := filepath.Join(destDir, relative)
		backup := filepath.Join(backupDir, relative)
		item := activatedFile{target: target, backup: backup}
		if current, statErr := os.Stat(target); statErr == nil {
			if current.IsDir() {
				return fmt.Errorf("runtime file collides with a directory: %s", relative)
			}
			item.existed = true
			if err := copyFileAtomic(target, backup, current.Mode().Perm()); err != nil {
				return err
			}
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
		activated = append(activated, item)
		if err := copyFileAtomic(source, target, info.Mode().Perm()); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		rollback()
		return func() {}, err
	}
	return rollback, nil
}

func copyFileAtomic(source, target string, mode os.FileMode) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	temporary := target + ".ligandx-new"
	if err := os.WriteFile(temporary, data, mode); err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, target); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func normalizedEntryName(name string) string {
	name = strings.TrimPrefix(filepath.ToSlash(name), "./")
	parts := strings.Split(name, "/")
	for len(parts) > 0 && parts[0] == "" {
		parts = parts[1:]
	}
	if len(parts) > 1 && strings.HasPrefix(parts[0], "ligand-x") {
		parts = parts[1:]
	}
	name = strings.Join(parts, "/")
	if name == "." || strings.Contains(name, "..") {
		return ""
	}
	return name
}

func EntryAllowed(name string) bool {
	allowedFiles := map[string]bool{
		"docker-compose.yml":        true,
		".env.production.template":  true,
		"LICENSE":                   true,
		"README.md":                 true,
		"docker/nginx/ligandx.conf": true,
		"config/rabbitmq.conf":      true,
		"config/flower_config.py":   true,
	}
	if allowedFiles[name] {
		return true
	}
	for _, prefix := range []string{"data/license/", "opt/deeppocket_models/"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
