//go:build !windows

package browserverify

import "os"

func sessionModeIsPrivate(mode os.FileMode) bool {
	return mode.Perm()&0o077 == 0
}

func sessionDirectoryModeHasOwnerAccess(mode os.FileMode) bool {
	return mode.Perm()&0o700 == 0o700
}
