package autostart

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

const supported = true

// read returns the entry's value, or "" when there is none.
func (e *Entry) read() (string, error) { return e.readValue(e.name) }

// readValue returns the value name under the entry's key, or "" when there is
// none.
func (e *Entry) readValue(name string) (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, e.key, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
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

// deleteValue removes the value name under the entry's key; one that isn't
// there is fine.
func (e *Entry) deleteValue(name string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, e.key, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}
