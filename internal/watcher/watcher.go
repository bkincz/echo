package watcher

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ---------------------------------------------------------------------------
// Watcher
// ---------------------------------------------------------------------------
type Watcher struct {
	fw        *fsnotify.Watcher
	onChange  func(paths []string)
	mu        sync.Mutex
	inputDirs map[string]struct{}
}

func New(onChange func(paths []string), dirs ...string) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	for _, dir := range dirs {
		if err := addRecursive(fw, dir); err != nil {
			fw.Close()
			return nil, err
		}
	}

	return &Watcher{
		fw:       fw,
		onChange: onChange,
	}, nil
}

// ---------------------------------------------------------------------------
// Internal
// ---------------------------------------------------------------------------
var skipDirs = map[string]bool{
	"node_modules": true,
	"dist":         true,
	".git":         true,
}

func addRecursive(fw *fsnotify.Watcher, dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		if skipDirs[d.Name()] {
			return filepath.SkipDir
		}
		return fw.Add(path)
	})
}

func (w *Watcher) WatchDir(dir string) error {
	return w.fw.Add(dir)
}

func (w *Watcher) WatchDirs(dirs []string) error {
	for _, dir := range dirs {
		if err := w.fw.Add(dir); err != nil {
			return err
		}
	}
	return nil
}

func (w *Watcher) SyncDirs(dirs []string) error {
	next := make(map[string]struct{}, len(dirs))
	for _, d := range dirs {
		next[d] = struct{}{}
	}

	w.mu.Lock()
	prev := w.inputDirs
	w.inputDirs = next
	w.mu.Unlock()

	for d := range next {
		if _, ok := prev[d]; !ok {
			if err := w.fw.Add(d); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	for d := range prev {
		if _, ok := next[d]; !ok {
			_ = w.fw.Remove(d)
		}
	}
	return nil
}

// Start begins watching in a background goroutine. Changes are debounced by
// 100 ms so that rapid editor saves produce a single callback.
func (w *Watcher) Start() {
	go func() {
		var (
			timer   *time.Timer
			timerCh <-chan time.Time
			mu      sync.Mutex
			pending = make(map[string]struct{})
		)

		for {
			select {
			case event, ok := <-w.fw.Events:
				if !ok {
					return
				}
				if !event.Has(fsnotify.Write) &&
					!event.Has(fsnotify.Create) &&
					!event.Has(fsnotify.Remove) &&
					!event.Has(fsnotify.Rename) {
					continue
				}

				if event.Has(fsnotify.Create) {
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						if err := addRecursive(w.fw, event.Name); err != nil {
							slog.Default().Error("watcher: add dir", "err", err)
						}
					}
				}

				mu.Lock()
				pending[event.Name] = struct{}{}
				if timer != nil {
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(100 * time.Millisecond)
					timerCh = timer.C
				} else {
					timer = time.NewTimer(100 * time.Millisecond)
					timerCh = timer.C
				}
				mu.Unlock()

			case <-timerCh:
				mu.Lock()
				paths := make([]string, 0, len(pending))
				for p := range pending {
					paths = append(paths, p)
					delete(pending, p)
				}
				timerCh = nil
				mu.Unlock()

				sort.Strings(paths)
				if w.onChange != nil {
					w.onChange(paths)
				}

			case err, ok := <-w.fw.Errors:
				if !ok {
					return
				}
				slog.Default().Error("watcher error", "err", err)
			}
		}
	}()
}

func (w *Watcher) Close() {
	_ = w.fw.Close()
}
