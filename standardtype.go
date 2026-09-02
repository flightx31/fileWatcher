package fileWatcher

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"
)

// SetStandardPollingInterval sets the period for the slow polling goroutine.
func (w *FileWatcher) SetStandardPollingInterval(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.standardInterval = d
}

// SetStandardFastPollingInterval sets the period for the fast polling goroutine.
func (w *FileWatcher) SetStandardFastPollingInterval(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.standardFastInterval = d
}

// SetStandardAggressiveness sets how aggressively to increase polling on hot files.
// A lower value makes files "hot" more easily.
func (w *FileWatcher) SetStandardAggressiveness(a float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.standardAggressiveness = a
}

// watchStandardFiles is the slow poller that works through the entire list.
func (w *FileWatcher) watchStandardFiles(done chan bool) {
	for {
		w.mu.RLock()
		interval := w.standardInterval
		w.mu.RUnlock()

		select {
		case <-done:
			return
		case <-time.After(interval):
			w.checkStandardChanges(false)
		}
	}
}

// watchStandardFastFiles is the fast poller for files changing quickly.
func (w *FileWatcher) watchStandardFastFiles(done chan bool) {
	for {
		w.mu.RLock()
		interval := w.standardFastInterval
		w.mu.RUnlock()

		select {
		case <-done:
			return
		case <-time.After(interval):
			w.checkStandardChanges(true)
		}
	}
}

func (w *FileWatcher) checkStandardChanges(isFast bool) {
	watchedPaths := w.standardWatchesMap.Keys()

	// For slow poll, we also want to detect deletions.
	seenPaths := make(map[string]bool)

	for _, rootPath := range watchedPaths {
		_ = afero.Walk(fs, rootPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}

			if isFast {
				// Fast poller only handles hot files
				meta, exists := w.standardMetadataMap.Get(path)
				if !exists || !w.isHot(meta) {
					return nil
				}

				// Try to lock
				if w.standardLockedFiles.SetIfAbsent(path, true) {
					w.checkStandardFileOrDir(path, info)
					w.standardLockedFiles.Remove(path)
				}
			} else {
				// Slow poller skips files locked by fast poller
				if w.standardLockedFiles.SetIfAbsent(path, true) {
					seenPaths[path] = true
					w.checkStandardFileOrDir(path, info)
					w.standardLockedFiles.Remove(path)
				} else {
					// Even if locked, it's seen
					seenPaths[path] = true
				}
			}
			return nil
		})
	}

	if !isFast {
		w.detectStandardDeletions(seenPaths, watchedPaths)
	}
}

func (w *FileWatcher) isHot(meta *StandardMetadata) bool {
	w.mu.RLock()
	agg := w.standardAggressiveness
	w.mu.RUnlock()

	// A simple heuristic: if it changed in the last 30 seconds and has more than 1 change,
	// or if ChangeCount > agg (where agg could be 5 or something).
	// Let's use: ChangeCount > agg
	return float64(meta.ChangeCount) >= agg
}

func (w *FileWatcher) checkStandardFileOrDir(path string, info os.FileInfo) {
	oldMeta, exists := w.standardMetadataMap.Get(path)

	if !exists {
		newMeta := &StandardMetadata{
			FileMetadata: FileMetadata{
				Path:    path,
				Size:    info.Size(),
				ModTime: info.ModTime(),
				IsDir:   info.IsDir(),
			},
			ChangeCount:    0,
			LastChangeTime: time.Time{},
		}
		if !newMeta.IsDir {
			newMeta.Hash, _ = w.calculateHash(path)
		}
		w.standardMetadataMap.Set(path, newMeta)

		e := FileWatcherEvent{Path: path}
		if newMeta.IsDir {
			e.Event = e.CreateFolderEvent()
		} else {
			e.Event = e.CreateFileEvent()
		}
		w.sendEvent(e)
		return
	}

	if info.IsDir() != oldMeta.IsDir {
		// Type changed
		w.reportStandardChange(path, oldMeta.IsDir, info.IsDir(), true)

		newMeta := &StandardMetadata{
			FileMetadata: FileMetadata{
				Path:    path,
				Size:    info.Size(),
				ModTime: info.ModTime(),
				IsDir:   info.IsDir(),
			},
		}
		if !newMeta.IsDir {
			newMeta.Hash, _ = w.calculateHash(path)
		}
		w.standardMetadataMap.Set(path, newMeta)
		return
	}

	if !info.IsDir() {
		if info.Size() != oldMeta.Size || !info.ModTime().Equal(oldMeta.ModTime) {
			newHash, _ := w.calculateHash(path)
			if newHash != oldMeta.Hash {
				e := FileWatcherEvent{Path: path, Event: FileWatcherEvent{}.EditFileEvent()}
				w.sendEvent(e)

				oldMeta.Size = info.Size()
				oldMeta.ModTime = info.ModTime()
				oldMeta.Hash = newHash
				oldMeta.ChangeCount++
				oldMeta.LastChangeTime = time.Now()
				w.standardMetadataMap.Set(path, oldMeta)
			} else {
				oldMeta.Size = info.Size()
				oldMeta.ModTime = info.ModTime()
				w.standardMetadataMap.Set(path, oldMeta)
			}
		} else {
			// No change detected. If it's been a while, maybe decay the ChangeCount?
			// For now, let's keep it simple.
			if time.Since(oldMeta.LastChangeTime) > 1*time.Minute && oldMeta.ChangeCount > 0 {
				oldMeta.ChangeCount--
				w.standardMetadataMap.Set(path, oldMeta)
			}
		}
	}
}

func (w *FileWatcher) reportStandardChange(path string, wasDir, isDir bool, isCreate bool) {
	eDel := FileWatcherEvent{Path: path}
	if wasDir {
		eDel.Event = eDel.DeleteFolderEvent()
	} else {
		eDel.Event = eDel.DeleteFileEvent()
	}
	w.sendEvent(eDel)

	if isCreate {
		eNew := FileWatcherEvent{Path: path}
		if isDir {
			eNew.Event = eNew.CreateFolderEvent()
		} else {
			eNew.Event = eNew.CreateFileEvent()
		}
		w.sendEvent(eNew)
	}
}

func (w *FileWatcher) detectStandardDeletions(seenPaths map[string]bool, watchedPaths []string) {
	for _, path := range w.standardMetadataMap.Keys() {
		if !seenPaths[path] {
			isStillStandard := false
			for _, root := range watchedPaths {
				if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
					isStillStandard = true
					break
				}
			}

			if isStillStandard {
				oldMeta, _ := w.standardMetadataMap.Get(path)
				e := FileWatcherEvent{Path: path}
				if oldMeta.IsDir {
					e.Event = e.DeleteFolderEvent()
				} else {
					e.Event = e.DeleteFileEvent()
				}
				w.sendEvent(e)
				w.standardMetadataMap.Remove(path)
				w.standardLockedFiles.Remove(path)
			}
		}
	}
}

func (w *FileWatcher) updateStandardMetadata(path string) {
	info, err := fs.Stat(path)
	if err != nil {
		return
	}

	meta := &StandardMetadata{
		FileMetadata: FileMetadata{
			Path:    path,
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   info.IsDir(),
		},
	}
	if !meta.IsDir {
		meta.Hash, _ = w.calculateHash(path)
	}
	w.standardMetadataMap.Set(path, meta)
}
