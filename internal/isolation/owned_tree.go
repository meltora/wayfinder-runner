package isolation

import (
	"errors"
	"io/fs"
	"os"

	"github.com/example/app-runner/internal/safeio"
)

func removeOwnedTree(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := safeio.MakeTreeRemovable(path); err != nil {
			return err
		}
	}
	return os.RemoveAll(path)
}
