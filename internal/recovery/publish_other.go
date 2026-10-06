//go:build !linux

package recovery

import "os"

func publishNoReplace(source, target string) error {
	if err := os.Link(source, target); err != nil {
		return err
	}
	return os.Remove(source)
}
