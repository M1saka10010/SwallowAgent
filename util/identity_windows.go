package util

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// Windows uses the filesystem default permissions. The agent does not manage ACLs.
func createPrivateKeyFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, nil, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	if err = checkPrivateKeyHandle(h); err != nil {
		windows.CloseHandle(h)
		return nil, incompletePrivateKeyError(path, err)
	}
	return os.NewFile(uintptr(h), path), nil
}

func openPrivateKeyFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	if err = checkPrivateKeyHandle(h); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

func checkPrivateKeyHandle(h windows.Handle) error {
	kind, err := windows.GetFileType(h)
	if err != nil {
		return err
	}
	if kind != windows.FILE_TYPE_DISK {
		return fmt.Errorf("private key must be a disk file")
	}
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return fmt.Errorf("private key must be a regular non-reparse file")
	}
	return nil
}
