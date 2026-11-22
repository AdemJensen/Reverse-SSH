package main

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockInstance(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errAlreadyRunning
		}
		return nil, err
	}
	return func() { file.Close() }, nil
}
