package mcpbridge

import "os"

func statSocket(path string) (os.FileInfo, error) { return os.Stat(path) }
