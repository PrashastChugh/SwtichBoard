package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"sync"
)

// TranscriptMeter reads Claude Code's JSONL transcript incrementally (only new
// bytes each time) and sums the usage blocks. Costs zero model tokens.
type TranscriptMeter struct {
	mu     sync.Mutex
	path   string
	offset int64
	tail   []byte
	byID   map[string][4]int64 // input, output, cache_read, cache_write
	last   [4]int64
	model  string
}

func newMeter(path string) *TranscriptMeter {
	return &TranscriptMeter{path: path, byID: map[string][4]int64{}}
}

type transcriptLine struct {
	UUID    string `json:"uuid"`
	Message *struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			Input       int64 `json:"input_tokens"`
			Output      int64 `json:"output_tokens"`
			CacheRead   int64 `json:"cache_read_input_tokens"`
			CacheCreate int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

func (m *TranscriptMeter) Update() {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, err := os.Stat(m.path)
	if err != nil {
		return
	}
	if st.Size() < m.offset { // rewritten: start over
		m.offset, m.tail, m.byID, m.last, m.model = 0, nil, map[string][4]int64{}, [4]int64{}, ""
	}
	if st.Size() == m.offset {
		return
	}
	f, err := os.Open(m.path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(m.offset, io.SeekStart); err != nil {
		return
	}
	chunk, err := io.ReadAll(f)
	if err != nil {
		return
	}
	m.offset += int64(len(chunk))
	data := append(m.tail, chunk...)
	lines := bytes.Split(data, []byte("\n"))
	m.tail = append([]byte{}, lines[len(lines)-1]...)
	for _, line := range lines[:len(lines)-1] {
		if !bytes.Contains(line, []byte(`"usage"`)) {
			continue
		}
		var tl transcriptLine
		if json.Unmarshal(line, &tl) != nil || tl.Message == nil || tl.Message.Usage == nil {
			continue
		}
		u := tl.Message.Usage
		rec := [4]int64{u.Input, u.Output, u.CacheRead, u.CacheCreate}
		key := tl.Message.ID
		if key == "" {
			key = tl.UUID
		}
		if key == "" {
			key = "n" + strconv.Itoa(len(m.byID))
		}
		m.byID[key] = rec // streaming can log a message more than once; keep the latest
		m.last = rec
		if tl.Message.Model != "" {
			m.model = tl.Message.Model
		}
	}
}

func (m *TranscriptMeter) Summary() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	var in, out, cr, cw int64
	for _, r := range m.byID {
		in += r[0]
		out += r[1]
		cr += r[2]
		cw += r[3]
	}
	total := in + cr + cw
	var hit any
	if total > 0 {
		hit = float64(int(float64(cr)/float64(total)*1000)) / 1000
	}
	return map[string]any{
		"input": in, "output": out, "cache_read": cr, "cache_write": cw,
		"context":   m.last[0] + m.last[2] + m.last[3], // most recent request = current context size
		"cache_hit": hit, "model": m.model, "messages": len(m.byID),
	}
}
