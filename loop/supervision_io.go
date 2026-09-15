package loop

import (
	"errors"
	"io"
	"os"
)

func readSupervisionFile(path string, limit int64) ([]byte, error) {
	file, err := openSupervisionFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readSupervisionOpenedFile(file, limit)
}

func readSupervisionOpenedFile(file *os.File, limit int64) ([]byte, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("loop: supervision file is not regular or is over budget")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = errors.New("loop: supervision file is over budget")
	}
	return data, err
}
