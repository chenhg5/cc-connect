//go:build windows

package grokbot

import "errors"

func mkfifo(string) error {
	return errors.New("mkfifo is not used on windows")
}
