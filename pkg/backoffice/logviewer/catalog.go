package logviewer

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type streamSummary struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Segments      int       `json:"segments"`
	TotalSize     int64     `json:"total_size"`
	NewestModTime time.Time `json:"newest_mod_time"`
}

func (v *Viewer) streams() ([]Stream, error) {
	root, err := filepath.Abs(v.opts.Root)
	if err != nil {
		return nil, err
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !rootInfo.IsDir() {
		return nil, fs.ErrInvalid
	}

	groups := map[string][]Segment{}
	addFile := func(path string, info fs.FileInfo) error {
		if info.IsDir() {
			return nil
		}
		if !v.opts.Recursive && filepath.Dir(path) != root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			return nil
		}
		if !matchesPrefixes(filepath.Base(rel), v.opts.Prefixes) {
			return nil
		}
		if !matchesGlobs(rel, v.opts.Globs) {
			return nil
		}
		name, rotation := logicalName(rel)
		groups[name] = append(groups[name], Segment{
			Path:    rel,
			Index:   rotation,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
		return nil
	}

	if v.opts.Recursive {
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			return addFile(path, info)
		})
	} else {
		entries, readErr := os.ReadDir(root)
		if readErr != nil {
			return nil, readErr
		}
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				return nil, err
			}
			if err := addFile(filepath.Join(root, entry.Name()), info); err != nil {
				return nil, err
			}
		}
	}
	if err != nil {
		return nil, err
	}

	streams := make([]Stream, 0, len(groups))
	for name, segments := range groups {
		sort.Slice(segments, func(i, j int) bool {
			return segments[i].Index > segments[j].Index
		})
		for i := range segments {
			segments[i].Index = i
		}
		streams = append(streams, Stream{ID: name, Name: name, Segments: segments})
	}
	sort.Slice(streams, func(i, j int) bool {
		return streams[i].Name < streams[j].Name
	})
	return streams, nil
}

func (v *Viewer) stream(id string) (Stream, error) {
	streams, err := v.streams()
	if err != nil {
		return Stream{}, err
	}
	for _, stream := range streams {
		if stream.ID == id {
			return stream, nil
		}
	}
	return Stream{}, errStreamNotFound
}

func (v *Viewer) segmentPath(seg Segment) (string, error) {
	root, err := filepath.Abs(v.opts.Root)
	if err != nil {
		return "", err
	}
	full := filepath.Join(root, seg.Path)
	clean, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, clean)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", fs.ErrPermission
	}
	return clean, nil
}

func matchesPrefixes(name string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func matchesGlobs(rel string, globs []string) bool {
	if len(globs) == 0 {
		return true
	}
	for _, glob := range globs {
		ok, _ := filepath.Match(glob, filepath.Base(rel))
		if ok {
			return true
		}
		ok, _ = filepath.Match(glob, rel)
		if ok {
			return true
		}
	}
	return false
}

var rotatedSuffixRE = regexp.MustCompile(`^(.*)\.([0-9]+)$`)

func logicalName(rel string) (string, int) {
	m := rotatedSuffixRE.FindStringSubmatch(rel)
	if len(m) != 3 {
		return rel, 0
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return rel, 0
	}
	return m[1], n
}

func summarize(streams []Stream) []streamSummary {
	out := make([]streamSummary, 0, len(streams))
	for _, stream := range streams {
		var total int64
		var newest time.Time
		for _, seg := range stream.Segments {
			total += seg.Size
			if seg.ModTime.After(newest) {
				newest = seg.ModTime
			}
		}
		out = append(out, streamSummary{
			ID:            stream.ID,
			Name:          stream.Name,
			Segments:      len(stream.Segments),
			TotalSize:     total,
			NewestModTime: newest,
		})
	}
	return out
}
