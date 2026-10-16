package devin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// prewarmModels loads a disk catalog and refreshes it in the background
// when it is missing or older than the TTL. It returns immediately.
// The real binary is not started unless LookPath can resolve it.
func (a *Agent) prewarmModels() {
	if a == nil {
		return
	}
	if _, err := exec.LookPath(a.modelsCommandName()); err != nil {
		return
	}
	a.loadDiskIfEmpty()
	if _, ok := a.cachedModelsFresh(); ok {
		return
	}
	a.refreshCatalog()
}

func (a *Agent) modelsCommandName() string {
	a.mu.RLock()
	name := strings.TrimSpace(a.modelsCmd)
	a.mu.RUnlock()
	if name == "" {
		return "devin"
	}
	return name
}

// refreshCatalog starts one background models-list fetch. Concurrent
// callers share that fetch. A finished fetch does not block the next one.
func (a *Agent) refreshCatalog() <-chan struct{} {
	done, start := a.beginFetch()
	if start {
		go a.runFetch(done)
	}
	return done
}

func (a *Agent) beginFetch() (chan struct{}, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fetchDone != nil {
		select {
		case <-a.fetchDone:
		default:
			return a.fetchDone, false
		}
	}
	done := make(chan struct{})
	a.fetchDone = done
	return done, true
}

func (a *Agent) fetchInFlight() <-chan struct{} {
	a.mu.RLock()
	done := a.fetchDone
	a.mu.RUnlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	default:
		return done
	}
}

func waitFetch(ctx context.Context, done <-chan struct{}) bool {
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	default:
	}
	select {
	case <-done:
		return true
	case <-ctx.Done():
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
}

func (a *Agent) runFetch(done chan struct{}) {
	defer close(done)
	ctx, cancel := context.WithTimeout(context.Background(), modelsFetchTimeout)
	defer cancel()
	out, err := a.modelsRunner()(ctx, a.modelsBinary(), a.modelsEnv())
	if err != nil {
		a.logFetchErr(err)
		return
	}
	opts, families, err := parseDevinModels(out)
	if err != nil {
		a.logFetchErr(err)
		return
	}
	if len(opts) == 0 {
		a.logFetchErr(errEmptyCatalog)
		return
	}
	a.storeModels(opts, families)
	if err := a.writeDiskCache(out); err != nil {
		slog.Debug("devin: models cache write failed", "error", err)
	}
}

func (a *Agent) logFetchErr(err error) {
	if _, ok := a.cachedModelsAny(); ok {
		slog.Debug("devin: models list refresh failed", "error", err)
		return
	}
	slog.Warn("devin: models list failed", "error", err)
}

func (a *Agent) loadDiskIfEmpty() {
	if a == nil {
		return
	}
	a.diskOnce.Do(func() {
		path := a.catalogPath()
		if path == "" {
			return
		}
		info, err := os.Stat(path)
		if err != nil {
			return
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return
		}
		opts, families, err := parseDevinModels(raw)
		if err != nil || len(opts) == 0 {
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if len(a.modelsCache) > 0 {
			return
		}
		a.modelsCache = cloneModels(opts)
		a.modelsCachedAt = info.ModTime()
		a.families = append([]catalogFamily(nil), families...)
	})
}

func (a *Agent) writeDiskCache(raw []byte) error {
	path := a.catalogPath()
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".devin-models-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

func (a *Agent) catalogPath() string {
	return catalogFilePath(a.modelsBinary(), a.effectiveXDG(), a.cacheDir())
}

func (a *Agent) cacheDir() string {
	if modelsCacheDir != "" {
		return modelsCacheDir
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "cc-connect")
}

func (a *Agent) effectiveXDG() string {
	a.mu.RLock()
	v := a.env["XDG_CONFIG_HOME"]
	a.mu.RUnlock()
	v = strings.TrimSpace(v)
	if v != "" {
		return v
	}
	return os.Getenv("XDG_CONFIG_HOME")
}

func catalogFilePath(binary, xdg, dir string) string {
	if dir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(binary + "\n" + xdg))
	name := "devin-models-" + hex.EncodeToString(sum[:6]) + ".json"
	return filepath.Join(dir, name)
}
