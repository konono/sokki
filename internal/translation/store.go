package translation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type store struct {
	root    string
	current map[int]Unit
}

func newStore(sourcePath string) *store {
	return &store{root: strings.TrimSuffix(sourcePath, ".txt"), current: map[int]Unit{}}
}

func (s *store) save(event, current Unit) error {
	s.current[current.ID] = current
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.root+".translation.jsonl", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := fmt.Fprintln(f, string(raw))
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	ids := make([]int, 0, len(s.current))
	for id := range s.current {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	var b strings.Builder
	for _, id := range ids {
		u := s.current[id]
		fmt.Fprintf(&b, "[unit=%d version=%d source=%v state=%s closed=%t reason=%s]\n", u.ID, u.Version, u.SourceIndices, u.State, u.Closed, u.CloseReason)
		if u.Japanese != "" {
			fmt.Fprintf(&b, "JA: %s\n", u.Japanese)
		} else {
			fmt.Fprintf(&b, "JA: （未訳: %s）\n", u.Error)
		}
		for _, line := range u.Source {
			fmt.Fprintf(&b, "EN: %s\n", line)
		}
		b.WriteByte('\n')
	}
	path := s.root + ".ja.txt"
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sokki-translation-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
