package fileWatcher

import (
	"io"
	"time"
)

// initStreaming initializes the streaming metadata and calls the callback.
func (w *FileWatcher) initStreaming(path string) {
	currentFs := w.getFsForPath(path)
	info, err := currentFs.Stat(path)
	if err != nil {
		logger.Error("Failed to stat streaming file", "path", path, "error", err)
		return
	}
	if info.IsDir() {
		logger.Warn("Streaming type is not supported for directories", "path", path)
		return
	}
	w.streamingOffsets.Set(path, info.Size())

	ch := make(chan []byte, 100)
	w.streamingChannels.Set(path, ch)

	w.mu.RLock()
	cb := w.OnStreamingDataCallback
	w.mu.RUnlock()

	if cb != nil {
		go cb(ch, path)
	}

	// Inactivity timer to revert to standard file
	go w.watchStreamingInactivity(path)
}

// readStreamingData reads new bytes from the file and sends them to the channel.
func (w *FileWatcher) readStreamingData(path string) {
	ch, ok := w.streamingChannels.Get(path)
	if !ok {
		return
	}

	offset, ok := w.streamingOffsets.Get(path)
	if !ok {
		return
	}

	currentFs := w.getFsForPath(path)
	f, err := currentFs.Open(path)
	if err != nil {
		logger.Error("Failed to open streaming file", "path", path, "error", err)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return
	}

	newSize := info.Size()
	if newSize <= offset {
		if newSize < offset {
			// File truncated? Reset offset.
			w.streamingOffsets.Set(path, 0)
		}
		return
	}

	_, err = f.Seek(offset, io.SeekStart)
	if err != nil {
		return
	}

	data := make([]byte, newSize-offset)
	_, err = io.ReadFull(f, data)
	if err != nil {
		logger.Error("Failed to read new data from streaming file", "path", path, "error", err)
		return
	}

	ch <- data
	w.streamingOffsets.Set(path, newSize)
}

// watchStreamingInactivity reverts the file to Standard after a period of no activity.
func (w *FileWatcher) watchStreamingInactivity(path string) {
	// Simple inactivity check: if offset doesn't change for a while, stop.
	lastOffset, _ := w.streamingOffsets.Get(path)

	// We check every 10 seconds, if it hasn't changed in 30 seconds total, we stop.
	inactiveCount := 0
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-w.done:
			return
		case <-ticker.C:
			// Check if it's still a streaming file
			if t, ok := w.GetFileType(path); !ok || t != Streaming {
				return
			}

			currentOffset, ok := w.streamingOffsets.Get(path)
			if !ok {
				return
			}

			if currentOffset == lastOffset {
				inactiveCount++
			} else {
				inactiveCount = 0
				lastOffset = currentOffset
			}

			if inactiveCount >= 3 { // 30 seconds
				logger.Info("Streaming stopped, reverting to Standard type", "path", path)
				_ = w.ConvertToFileType(path, Standard)
				return
			}
		}
	}
}
