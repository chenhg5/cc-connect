package localapi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type privateSecurity struct {
	sid  *windows.SID
	sd   *windows.SECURITY_DESCRIPTOR
	sddl string
}

func currentSecurity() (privateSecurity, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return privateSecurity{}, fmt.Errorf("read local API owner: %w", err)
	}
	sddl := "O:" + user.User.Sid.String() + "D:P(A;;GA;;;SY)(A;;GA;;;" + user.User.Sid.String() + ")"
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return privateSecurity{}, fmt.Errorf("create local API security descriptor: %w", err)
	}
	return privateSecurity{sid: user.User.Sid, sd: sd, sddl: sddl}, nil
}

func (s privateSecurity) attributes() *windows.SecurityAttributes {
	return &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: s.sd,
	}
}

func localFilePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if len(filepath.VolumeName(abs)) != 2 || strings.HasPrefix(abs, `\\`) {
		return "", fmt.Errorf("local API state requires an absolute local drive path")
	}
	return abs, nil
}

func wideFilePath(path string) (*uint16, error) {
	if !strings.HasPrefix(path, `\\?\`) {
		path = `\\?\` + path
	}
	return windows.UTF16PtrFromString(path)
}

// Refuse directory redirection before writing an endpoint or lock file.
func rejectReparsePath(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		wide, err := wideFilePath(current)
		if err != nil {
			return err
		}
		attributes, err := windows.GetFileAttributes(wide)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fmt.Errorf("local API state must not traverse a reparse point")
		}
		if parent := filepath.Dir(current); parent == current {
			return nil
		}
	}
}

func (s privateSecurity) checkOwner(handle windows.Handle) error {
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	if sd == nil {
		return fmt.Errorf("local API state has no security descriptor")
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(s.sid) {
		return fmt.Errorf("local API state belongs to another Windows identity")
	}
	return nil
}

func (s privateSecurity) protectHandle(handle windows.Handle) error {
	if err := s.checkOwner(handle); err != nil {
		return err
	}
	dacl, _, err := s.sd.DACL()
	if err != nil {
		return err
	}
	if dacl == nil {
		return fmt.Errorf("local API state has no private DACL")
	}
	return windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
}

func (s privateSecurity) ensureRunDirectory(path string) error {
	if err := rejectReparsePath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	wide, err := wideFilePath(path)
	if err != nil {
		return err
	}
	if err := windows.CreateDirectory(wide, s.attributes()); err != nil && err != windows.ERROR_ALREADY_EXISTS {
		return err
	}
	handle, err := windows.CreateFile(wide, windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("local API run path is not a regular directory")
	}
	return s.protectHandle(handle)
}

func (s privateSecurity) openFile(path string, access, sharing, disposition uint32) (*os.File, error) {
	wide, err := wideFilePath(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(wide, access|windows.READ_CONTROL, sharing,
		s.attributes(), disposition, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open local API state", Path: path, Err: err}
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("local API state must be a regular file")
	}
	if err := s.checkOwner(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}
