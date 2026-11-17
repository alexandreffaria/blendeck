package main

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// claimInstance takes a per-user named mutex for the page's address. first
// is false when another copy holds it; release frees it.
func claimInstance(addr string) (release func(), first bool, err error) {
	name := `Local\Consort-` + strings.NewReplacer(`\`, "_").Replace(addr)
	h, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(name))
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(h)
		return func() {}, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("check for another copy of Consort: %w", err)
	}
	return func() { windows.CloseHandle(h) }, true, nil
}
