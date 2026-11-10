package project

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var ErrLockTimeout = errors.New("lock wait timed out")

// Lock is an flock(2) on a lock file: the kernel frees it when the holding process dies. The file exists while held
// (Unlock removes it) and holds the holder's pid, for the waiting line only. Keep a held Lock referenced: a collected
// *os.File is closed, which frees the flock.
type Lock struct {
	Path   string
	Output io.Writer
	f      *os.File
}

func NewLock(path string) *Lock { return &Lock{Path: path, Output: os.Stdout} }

// Try takes the lock once; false while another open file (in any process) holds it, or this Lock already does.
func (l *Lock) Try() (bool, error) {
	if l.f != nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o755); err != nil {
		return false, err
	}
	for {
		f, err := os.OpenFile(l.Path, os.O_RDWR|os.O_CREATE, 0o644)
		if err != nil {
			return false, err
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return false, nil
			}
			return false, err
		}
		// The previous holder removed the file on Unlock after we opened it: this inode is not the lock.
		if !samePath(f, l.Path) {
			f.Close()
			continue
		}
		l.f = f
		_ = l.Write([]byte(strconv.Itoa(os.Getpid()) + "\n"))
		return true, nil
	}
}

// Write replaces the held lock file's content.
func (l *Lock) Write(b []byte) error {
	if l.f == nil {
		return errors.New("lock not held: " + l.Path)
	}
	if err := l.f.Truncate(0); err != nil {
		return err
	}
	_, err := l.f.WriteAt(b, 0)
	return err
}

// Holder is the pid the lock file names ("" when there is none).
func (l *Lock) Holder() string {
	b, _ := os.ReadFile(l.Path)
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// Wait retries until it acquires the lock, ctx is cancelled, or d elapses. A zero d makes one try;
// a negative d waits until ctx is cancelled. It reports the holder every 30 seconds.
func (l *Lock) Wait(ctx context.Context, d time.Duration) error {
	deadline := time.Time{}
	if d >= 0 {
		deadline = time.Now().Add(d)
	}
	var timeout <-chan time.Time
	var timer *time.Timer
	if d > 0 {
		timer = time.NewTimer(d)
		timeout = timer.C
		defer timer.Stop()
	}
	print := time.NewTicker(30 * time.Second)
	defer print.Stop()
	for {
		ok, err := l.Try()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if d == 0 {
			return ErrLockTimeout
		}
		wait := 100 * time.Millisecond
		if !deadline.IsZero() {
			left := time.Until(deadline)
			if left <= 0 {
				return ErrLockTimeout
			}
			if left < wait {
				wait = left
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return ErrLockTimeout
		case <-print.C:
			l.reportHolder()
		case <-time.After(wait):
		}
	}
}

// Unlock removes the lock file and releases the lock; a no-op when this Lock does not hold it.
func (l *Lock) Unlock() error {
	if l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	var err error
	if samePath(f, l.Path) {
		if e := os.Remove(l.Path); e != nil && !errors.Is(e, os.ErrNotExist) {
			err = e
		}
	}
	if e := f.Close(); err == nil {
		err = e
	}
	return err
}

// samePath: path still names f's file (not removed, not replaced).
func samePath(f *os.File, path string) bool {
	a, err1 := f.Stat()
	b, err2 := os.Stat(path)
	return err1 == nil && err2 == nil && os.SameFile(a, b)
}

// PIDAlive: the process exists (signal 0 delivered, or refused for permission).
func PIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (l *Lock) reportHolder() {
	pid, _ := strconv.Atoi(l.Holder())
	if pid <= 0 {
		return
	}
	writer := l.Output
	if writer == nil {
		writer = os.Stdout
	}
	_, _ = fmt.Fprintf(writer, "waiting for %s: pid %d\n", filepath.Base(l.Path), pid)
}

// Held: some open file holds the lock (this Lock, or another in any process). A probe: it neither takes the lock nor
// creates or removes the file.
func (l *Lock) Held() bool {
	if l.f != nil {
		return true
	}
	f, err := os.Open(l.Path)
	if err != nil {
		return false
	}
	defer f.Close()
	return errors.Is(syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB), syscall.EWOULDBLOCK)
}
