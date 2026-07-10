package logviewer

import (
	"io/fs"
	"os"
)

type seekReadCloser interface {
	Read([]byte) (int, error)
	Seek(int64, int) (int64, error)
	Close() error
}

func fsOpen(path string) (seekReadCloser, error) {
	return os.Open(path)
}

func fsStat(path string) (fs.FileInfo, error) {
	return os.Stat(path)
}
