package fileWatcher

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"
)

// SetArchivePollingInterval sets the period for checking archive files.
func (w *FileWatcher) SetArchivePollingInterval(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.archiveInterval = d
}

// watchArchiveFiles periodically scans all paths registered as Archive.
func (w *FileWatcher) watchArchiveFiles(done chan bool) {
	for {
		w.mu.RLock()
		interval := w.archiveInterval
		w.mu.RUnlock()

		select {
		case <-done:
			return
		case <-time.After(interval):
			w.checkArchiveChanges()
		}
	}
}

func (w *FileWatcher) checkArchiveChanges() {
	// We need to check both the explicitly watched paths and their contents if they are directories.
	// Also need to detect deletions of previously known files.

	watchedPaths := w.ArchiveWatchesMap.Keys()
	
	// Track paths seen in this iteration to detect deletions
	seenPaths := make(map[string]bool)

	for _, rootPath := range watchedPaths {
		_ = afero.Walk(fs, rootPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // Skip items that can't be accessed
			}
			
			seenPaths[path] = true
			w.checkFileOrDir(path, info)
			return nil
		})
	}

	// Detect deletions: metadata exists but path was not seen in any watched root
	for _, path := range w.archiveMetadataMap.Keys() {
		if !seenPaths[path] {
			// Check if this path is still supposed to be watched (belonging to one of the root paths)
			// Actually, if it's in metadata but not seen in walk, it's deleted.
			
			// We should only report deletion if it's under one of the currently watched Archive roots
			isStillArchive := false
			for _, root := range watchedPaths {
				if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
					isStillArchive = true
					break
				}
			}
			
			if isStillArchive {
				oldMeta, _ := w.archiveMetadataMap.Get(path)
				e := FileWatcherEvent{Path: path}
				if oldMeta.IsDir {
					e.Event = e.DeleteFolderEvent()
				} else {
					e.Event = e.DeleteFileEvent()
				}
				w.sendEvent(e)
				w.archiveMetadataMap.Remove(path)
			}
		}
	}
}

// stringsHasPrefix is a helper because strings might not be imported or I can just use strings.HasPrefix
// I'll import strings.
func (w *FileWatcher) checkFileOrDir(path string, info os.FileInfo) {
	oldMeta, exists := w.archiveMetadataMap.Get(path)
	
	if !exists {
		// New file discovered
		newMeta := &FileMetadata{
			Path:    path,
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   info.IsDir(),
		}
		if !newMeta.IsDir {
			newMeta.Hash, _ = w.calculateHash(path)
		}
		w.archiveMetadataMap.Set(path, newMeta)
		
		// Trigger CREATE event
		e := FileWatcherEvent{Path: path}
		if newMeta.IsDir {
			e.Event = e.CreateFolderEvent()
		} else {
			e.Event = e.CreateFileEvent()
		}
		w.sendEvent(e)
		return
	}

	// Check for changes
	if info.IsDir() != oldMeta.IsDir {
		// Type changed (e.g. file replaced by dir) - handle as delete then create
		eDel := FileWatcherEvent{Path: path}
		if oldMeta.IsDir {
			eDel.Event = eDel.DeleteFolderEvent()
		} else {
			eDel.Event = eDel.DeleteFileEvent()
		}
		w.sendEvent(eDel)
		
		eNew := FileWatcherEvent{Path: path}
		if info.IsDir() {
			eNew.Event = eNew.CreateFolderEvent()
		} else {
			eNew.Event = eNew.CreateFileEvent()
		}
		w.sendEvent(eNew)
		
		// Update metadata
		newMeta := &FileMetadata{
			Path:    path,
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   info.IsDir(),
		}
		if !newMeta.IsDir {
			newMeta.Hash, _ = w.calculateHash(path)
		}
		w.archiveMetadataMap.Set(path, newMeta)
		return
	}

	if !info.IsDir() {
		// Check file changes: size or mod time
		if info.Size() != oldMeta.Size || !info.ModTime().Equal(oldMeta.ModTime) {
			// Possibly changed, check hash for confirmation
			newHash, _ := w.calculateHash(path)
			if newHash != oldMeta.Hash {
				e := FileWatcherEvent{Path: path, Event: FileWatcherEvent{}.EditFileEvent()}
				w.sendEvent(e)
				
				// Update metadata
				oldMeta.Size = info.Size()
				oldMeta.ModTime = info.ModTime()
				oldMeta.Hash = newHash
				w.archiveMetadataMap.Set(path, oldMeta)
			} else {
				// ModTime/Size changed but hash is same (e.g. touch or same content written)
				// We might still want to update ModTime/Size to avoid repeated hashing
				oldMeta.Size = info.Size()
				oldMeta.ModTime = info.ModTime()
				w.archiveMetadataMap.Set(path, oldMeta)
			}
		}
	}
}

func (w *FileWatcher) calculateHash(path string) (string, error) {
	f, err := fs.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

func (w *FileWatcher) updateArchiveMetadata(path string) {
	info, err := fs.Stat(path)
	if err != nil {
		return
	}
	
	// If it's a directory, we'll discover its content in the next poll cycle.
	// For now, just add the path itself.
	meta := &FileMetadata{
		Path:    path,
		Size:    info.Size(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}
	if !meta.IsDir {
		meta.Hash, _ = w.calculateHash(path)
	}
	w.archiveMetadataMap.Set(path, meta)
}
