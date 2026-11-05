// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0

package jobs

import "golang.org/x/sys/windows"

func publishMetadata(temporary, path string) error {
	source, err := windows.UTF16PtrFromString(temporary)
	if err != nil {
		return err
	}
	destination, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(source, destination, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
