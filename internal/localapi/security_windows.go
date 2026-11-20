package localapi

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
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
	return s.checkDescriptorOwner(sd)
}

func (s privateSecurity) checkDescriptorOwner(sd *windows.SECURITY_DESCRIPTOR) error {
	if sd == nil || !sd.IsValid() {
		return fmt.Errorf("local API state has no valid security descriptor")
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

func (s privateSecurity) checkPrivateDescriptor(sd *windows.SECURITY_DESCRIPTOR) error {
	defer runtime.KeepAlive(sd)
	if err := s.checkDescriptorOwner(sd); err != nil {
		return err
	}
	rejected := func() error {
		return fmt.Errorf("local API state has a permissive or unsupported DACL")
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return rejected()
	}
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			return err
		}
		if ace == nil || uintptr(ace.Header.AceSize) < unsafe.Sizeof(windows.ACE_HEADER{}) {
			return rejected()
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return rejected()
		}
		sidOffset := unsafe.Offsetof(ace.SidStart)
		if uintptr(ace.Header.AceSize) < sidOffset+8 {
			return rejected()
		}
		sidBytes := unsafe.Slice((*byte)(unsafe.Pointer(&ace.SidStart)), int(ace.Header.AceSize)-int(sidOffset))
		if sidBytes[1] > 15 || 8+4*int(sidBytes[1]) > len(sidBytes) {
			return rejected()
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || (ace.Mask != 0 && !sid.Equals(s.sid) && !sid.IsWellKnown(windows.WinLocalSystemSid)) {
			return rejected()
		}
	}
	return nil
}

func (s privateSecurity) checkPrivateHandle(handle windows.Handle) error {
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	return s.checkPrivateDescriptor(sd)
}

// Authenticate the connected peer before exposing the connection to HTTP code.
// go-winio's pipe embeds win32File, which exposes its live handle through Fd.
func (s privateSecurity) checkPipeServer(conn net.Conn, expectedPID uint32) error {
	defer runtime.KeepAlive(conn)
	pipe, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return fmt.Errorf("local API pipe does not expose a verifiable handle")
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(pipe.Fd()), &pid); err != nil {
		return fmt.Errorf("read local API pipe server identity: %w", err)
	}
	if pid != expectedPID {
		return fmt.Errorf("local API pipe server PID does not match its descriptor")
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return fmt.Errorf("open local API pipe server identity: %w", err)
	}
	defer func() { _ = windows.CloseHandle(process) }()
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return fmt.Errorf("read local API pipe server token: %w", err)
	}
	defer func() { _ = token.Close() }()
	user, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("read local API pipe server user: %w", err)
	}
	if !user.User.Sid.Equals(s.sid) {
		return fmt.Errorf("local API pipe server belongs to another Windows identity")
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
	if err := s.checkPrivateHandle(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}
