package main

import (
	"os"
	"time"
)

func chtimes(path string, atime, mtime time.Time) error {
	return os.Chtimes(path, atime, mtime)
}
