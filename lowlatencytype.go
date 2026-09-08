package fileWatcher

import (
	"os"

	"github.com/spf13/afero"
)

// addLowLatencyRecursive walks the directory and adds all subdirectories to the fsnotify watcher.
func (w *FileWatcher) addLowLatencyRecursive(path string) error {
	currentFs := w.getFsForPath(path)
	return afero.Walk(currentFs, path, func(subPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			err = w.fsNotify.Add(subPath)
			if err != nil {
				logger.Error("Failed to add directory to low latency watcher", "path", subPath, "error", err)
			}
		}
		return nil
	})
}
