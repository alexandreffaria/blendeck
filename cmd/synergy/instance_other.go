//go:build !windows

package main

// claimInstance always says this is the first copy: elsewhere a second copy
// finds the ports busy and says so.
func claimInstance(string) (release func(), first bool, err error) {
	return func() {}, true, nil
}
