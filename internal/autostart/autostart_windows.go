package autostart

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

const supported = true

// read returns the entry's value, or "" when there is none.
func (e *Entry) read() (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, e.key, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer k.Close()
	v, _, err := k.GetStringValue(e.name)
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	return v, err
}

func (e *Entry) write() error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, e.key, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(e.name, e.command)
}

func (e *Entry) remove() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, e.key, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(e.name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}
