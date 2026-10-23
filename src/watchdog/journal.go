// HYDRA-UMC-NODE-HEALING - watchdog package: journal.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// A persistent, append-only journal of what the watchdog saw and did.
//
// Everything the reactors print (every state transition of a node, every recovery request, every request that
// was held back by a safeguard) is also written as one JSON object per line, so that after a restart or a
// reboot there is still a record of why a node was restarted. Each line carries the SHA-256 of the line before
// it, so a line removed or edited afterwards breaks the chain and VerifyJournal says where. When the file
// reaches MaxBytes it is rotated (journal.jsonl -> journal.jsonl.1 -> ...), the chain continuing in the new
// file, and Backups rotated files are kept.
//
// The journal is opt-in (-journal PATH). A line that cannot be written is reported on the console and never
// stops the watchdog: detection and recovery matter more than the record of them.
package watchdog

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/JuanenRac/hydra-umc-node-healing/src/redact"
)

const (
	// DefaultJournalMaxBytes is the size at which the journal is rotated.
	DefaultJournalMaxBytes = 5 * 1024 * 1024
	// DefaultJournalBackups is how many rotated files are kept.
	DefaultJournalBackups = 5
	journalGenesis        = "0000000000000000000000000000000000000000000000000000000000000000"
)

// Journal appends lines to a file. Use NewJournal; the zero value is not usable.
type Journal struct {
	Path     string
	MaxBytes int64
	Backups  int
	// Now defaults to time.Now - overridable for tests.
	Now func() time.Time

	mu       sync.Mutex
	previous string
}

// NewJournal validates the options. maxBytes below 1024 and a negative or absurd number of backups are refused.
func NewJournal(path string, maxBytes int64, backups int) (*Journal, error) {
	if path == "" {
		return nil, errors.New("journal: the path is empty")
	}
	if maxBytes < 1024 {
		return nil, fmt.Errorf("journal: max bytes must be at least 1024, got %d", maxBytes)
	}
	if backups < 0 || backups > 100 {
		return nil, fmt.Errorf("journal: backups must be between 0 and 100, got %d", backups)
	}
	return &Journal{Path: path, MaxBytes: maxBytes, Backups: backups}, nil
}

func digestOf(line []byte) string {
	sum := sha256.Sum256(line)
	return hex.EncodeToString(sum[:])
}

func lastLine(path string) []byte {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	var last []byte
	reader := bufio.NewReaderSize(file, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if trimmed := trimNewline(line); len(trimmed) > 0 {
			last = append(last[:0], trimmed...)
		}
		if err != nil {
			break
		}
	}
	return last
}

func trimNewline(line []byte) []byte {
	for len(line) > 0 && (line[len(line)-1] == '\n' || line[len(line)-1] == '\r') {
		line = line[:len(line)-1]
	}
	return line
}

func (j *Journal) rotate() error {
	if j.Backups == 0 {
		return os.Remove(j.Path)
	}
	_ = os.Remove(j.Path + "." + strconv.Itoa(j.Backups))
	for index := j.Backups - 1; index >= 1; index-- {
		from := j.Path + "." + strconv.Itoa(index)
		if _, err := os.Stat(from); err == nil {
			if err := os.Rename(from, j.Path+"."+strconv.Itoa(index+1)); err != nil {
				return err
			}
		}
	}
	return os.Rename(j.Path, j.Path+".1")
}

// Record appends one line {time, event, fields..., prev}.
func (j *Journal) Record(event string, fields map[string]any) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now
	if j.Now != nil {
		now = j.Now
	}
	body := map[string]any{"event": event, "time": now().UTC().Format("2006-01-02T15:04:05Z")}
	for key, value := range fields {
		if key != "event" && key != "time" && key != "prev" {
			// nothing that reaches the journal keeps a secret: the text of a probe can carry whatever a node answered
			body[key] = redact.JSON(value)
		}
	}
	if err := os.MkdirAll(filepath.Dir(j.Path), 0o750); err != nil {
		j.previous = ""
		return err
	}
	if j.previous == "" {
		j.previous = journalGenesis
		if last := lastLine(j.Path); last != nil {
			j.previous = digestOf(last)
		} else if last := lastLine(j.Path + ".1"); last != nil {
			j.previous = digestOf(last)
		}
	}
	body["prev"] = j.previous
	line, err := json.Marshal(body) // maps are marshalled with sorted keys
	if err != nil {
		return err
	}
	if info, err := os.Stat(j.Path); err == nil && info.Size()+int64(len(line))+1 > j.MaxBytes {
		if err := j.rotate(); err != nil {
			j.previous = ""
			return err
		}
	}
	file, err := os.OpenFile(j.Path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o640)
	if err != nil {
		j.previous = ""
		return err
	}
	_, writeErr := file.Write(append(line, '\n'))
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		j.previous = ""
		return errors.Join(writeErr, closeErr)
	}
	j.previous = digestOf(line)
	return nil
}

// Printf returns a print function with the signature the reactors use: it forwards to next (fmt.Printf when
// nil) and writes the same text, without its final newline, as a "log" line. A journal failure is printed to
// standard error once per failure and otherwise ignored.
func (j *Journal) Printf(next func(format string, args ...any)) func(format string, args ...any) {
	if next == nil {
		next = func(format string, args ...any) { fmt.Printf(format, args...) }
	}
	return func(format string, args ...any) {
		next(format, args...)
		message := string(trimNewline([]byte(fmt.Sprintf(format, args...))))
		if err := j.Record("log", map[string]any{"message": message}); err != nil {
			fmt.Fprintf(os.Stderr, "[node-healing] journal: cannot write %s: %v\n", j.Path, err)
		}
	}
}

// Files lists the journal files, oldest first.
func (j *Journal) Files() []string {
	var files []string
	for index := j.Backups; index >= 1; index-- {
		name := j.Path + "." + strconv.Itoa(index)
		if _, err := os.Stat(name); err == nil {
			files = append(files, name)
		}
	}
	if _, err := os.Stat(j.Path); err == nil {
		files = append(files, j.Path)
	}
	return files
}

// VerifyJournal checks the hash chain over the journal and its rotated files. It returns the number of lines
// read and, when the chain is broken, an error that says where.
func (j *Journal) VerifyJournal() (int, error) {
	previous := ""
	count := 0
	for _, name := range j.Files() {
		file, err := os.Open(name)
		if err != nil {
			return count, err
		}
		reader := bufio.NewReaderSize(file, 64*1024)
		number := 0
		for {
			raw, readErr := reader.ReadBytes('\n')
			number++
			if line := trimNewline(raw); len(line) > 0 {
				var record map[string]any
				if err := json.Unmarshal(line, &record); err != nil {
					file.Close()
					return count, fmt.Errorf("%s:%d: not valid JSON", filepath.Base(name), number)
				}
				if previous != "" && record["prev"] != previous {
					file.Close()
					return count, fmt.Errorf("%s:%d: the line before it was changed or removed", filepath.Base(name), number)
				}
				previous = digestOf(line)
				count++
			}
			if readErr != nil {
				if readErr != io.EOF {
					file.Close()
					return count, readErr
				}
				break
			}
		}
		file.Close()
	}
	return count, nil
}
