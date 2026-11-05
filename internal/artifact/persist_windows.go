// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0
package artifact

import "golang.org/x/sys/windows"

func moveToDeletionStage(source, destination string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}
