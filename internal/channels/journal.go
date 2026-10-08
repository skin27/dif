package channels

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
)

type record struct {
	Op        string          `json:"op"`
	Queue     string          `json:"queue,omitempty"`
	Target    string          `json:"target,omitempty"`
	ID        uint64          `json:"id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Attempts  int             `json:"attempts,omitempty"`
	Available int64           `json:"available,omitempty"`
	Parked    bool            `json:"parked,omitempty"`
	Key       string          `json:"key,omitempty"`
	Expires   int64           `json:"expires,omitempty"`
}

func frame(rec record) ([]byte, error) {
	data, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	b := make([]byte, 16+len(data))
	copy(b, "DIF1")
	binary.BigEndian.PutUint32(b[4:8], uint32(len(data)))
	binary.BigEndian.PutUint32(b[8:12], crc32.ChecksumIEEE(data))
	binary.BigEndian.PutUint32(b[12:16], crc32.ChecksumIEEE(b[:12]))
	copy(b[16:], data)
	return b, nil
}

func (r *Runtime) openJournal() error {
	if err := os.MkdirAll(r.config.Directory, 0700); err != nil {
		return err
	}
	lock, err := lockDirectory(r.config.Directory)
	if err != nil {
		return fmt.Errorf("storage directory already owned or inaccessible: %w", err)
	}
	r.lock = lock
	path := filepath.Join(r.config.Directory, "channels.journal")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	r.file = f
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > r.config.MaxDiskBytes/2 {
		return fmt.Errorf("existing journal exceeds configured disk limit")
	}
	for {
		var header [16]byte
		n, err := io.ReadFull(f, header[:])
		if err == io.EOF {
			break
		}
		if err == io.ErrUnexpectedEOF {
			if err := f.Truncate(r.journalBytes); err != nil {
				return err
			}
			break
		}
		if err != nil {
			return err
		}
		if string(header[:4]) != "DIF1" || crc32.ChecksumIEEE(header[:12]) != binary.BigEndian.Uint32(header[12:]) {
			return fmt.Errorf("corrupt journal header at %d", r.journalBytes)
		}
		size := binary.BigEndian.Uint32(header[4:8])
		if size > uint32(r.config.MaxMessageBytes+64<<10) {
			return fmt.Errorf("journal record exceeds configured message limit")
		}
		data := make([]byte, size)
		_, err = io.ReadFull(f, data)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			if err := f.Truncate(r.journalBytes); err != nil {
				return err
			}
			break
		}
		if err != nil {
			return err
		}
		if crc32.ChecksumIEEE(data) != binary.BigEndian.Uint32(header[8:12]) {
			return fmt.Errorf("corrupt journal record at %d", r.journalBytes)
		}
		var rec record
		if err := json.Unmarshal(data, &rec); err != nil {
			return err
		}
		if err := r.replay(rec); err != nil {
			return err
		}
		r.journalBytes += int64(n) + int64(size)
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return syncDirectory(r.config.Directory)
}

func (r *Runtime) replay(rec record) error {
	if rec.ID > r.nextID {
		r.nextID = rec.ID
	}
	if rec.Op == "meta" {
		return nil
	}
	if rec.Op == "key" {
		cfg, ok := r.config.Idempotency[rec.Queue]
		if !ok || !cfg.Durable {
			return fmt.Errorf("journal requires durable idempotency namespace %q", rec.Queue)
		}
		r.keys[rec.Queue][rec.Key] = &keyState{expires: rec.Expires}
		return nil
	}
	q := r.queues[rec.Queue]
	if q == nil || !q.Durable() {
		return fmt.Errorf("journal requires durable queue %q", rec.Queue)
	}
	if rec.Op == "put" {
		if _, err := decodeMessage(rec.Data); err != nil {
			return err
		}
		q.items = append(q.items, &stored{Entry: Entry{ID: rec.ID}, data: rec.Data, attempts: rec.Attempts, available: rec.Available, parked: rec.Parked})
		return nil
	}
	for i, e := range q.items {
		if e.ID != rec.ID {
			continue
		}
		switch rec.Op {
		case "ack":
			q.remove(i)
		case "attempt":
			e.attempts = rec.Attempts
		case "retry":
			e.available = rec.Available
		case "park":
			e.parked = true
		case "move":
			dst := r.queues[rec.Target]
			if dst == nil || !dst.Durable() {
				return fmt.Errorf("journal requires durable queue %q", rec.Target)
			}
			if _, err := decodeMessage(rec.Data); err != nil {
				return err
			}
			dst.items = append(dst.items, &stored{Entry: Entry{ID: e.ID}, data: rec.Data})
			q.remove(i)
		default:
			return fmt.Errorf("unknown journal operation %q", rec.Op)
		}
		return nil
	}
	return fmt.Errorf("journal references unknown delivery %d", rec.ID)
}

func (r *Runtime) append(rec record) error {
	if r.file == nil {
		return fmt.Errorf("durable storage not open")
	}
	b, err := frame(rec)
	if err != nil {
		return err
	}
	// Reserve half the directory budget for the compacted replacement and
	// half of the active journal for settlement records (ack/retry/DLQ).
	limit := r.config.MaxDiskBytes / 2
	if rec.Op == "put" || rec.Op == "key" {
		limit /= 2
	}
	if r.journalBytes+int64(len(b)) > limit {
		if err := r.compact(); err != nil {
			return err
		}
		if r.journalBytes+int64(len(b)) > limit {
			return fmt.Errorf("channel storage capacity exceeded")
		}
	}
	if _, err := r.file.Write(b); err != nil {
		return r.fail(err)
	}
	if err := r.file.Sync(); err != nil {
		return r.fail(err)
	}
	r.journalBytes += int64(len(b))
	return nil
}

// compact replaces the journal only after the complete snapshot is synced.
// The directory ownership lock spans the close/replace/reopen sequence.
func (r *Runtime) compact() error {
	path := filepath.Join(r.config.Directory, "channels.compact")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return r.fail(err)
	}
	var size int64
	write := func(rec record) error {
		b, err := frame(rec)
		if err != nil {
			return err
		}
		size += int64(len(b))
		if size > r.config.MaxDiskBytes/2 {
			return fmt.Errorf("compacted journal exceeds disk limit")
		}
		_, err = f.Write(b)
		return err
	}
	err = write(record{Op: "meta", ID: r.nextID})
	for name, q := range r.queues {
		if !q.Durable() {
			continue
		}
		for _, e := range q.items {
			if err == nil {
				err = write(record{Op: "put", Queue: name, ID: e.ID, Data: e.data, Attempts: e.attempts, Available: e.available, Parked: e.parked})
			}
		}
	}
	r.expireKeys()
	for namespace, keys := range r.keys {
		if !r.config.Idempotency[namespace].Durable {
			continue
		}
		for key, state := range keys {
			if !state.pending && err == nil {
				err = write(record{Op: "key", Queue: namespace, Key: key, Expires: state.expires})
			}
		}
	}
	err = errors.Join(err, f.Sync(), f.Close())
	if err != nil {
		return r.fail(err)
	}
	if err := r.file.Close(); err != nil {
		return r.fail(err)
	}
	r.file = nil
	if err := replaceJournal(path, filepath.Join(r.config.Directory, "channels.journal")); err != nil {
		return r.fail(err)
	}
	if err := syncDirectory(r.config.Directory); err != nil {
		return r.fail(err)
	}
	r.file, err = os.OpenFile(filepath.Join(r.config.Directory, "channels.journal"), os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return r.fail(err)
	}
	r.journalBytes = size
	return nil
}
