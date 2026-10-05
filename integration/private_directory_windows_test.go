package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func windowsScratchUser(t *testing.T) string {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return user.User.Sid.String()
}

func createWindowsScratchFixture(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	if err := windows.CreateDirectory(name, &sa); err != nil {
		t.Fatal(err)
	}
}

func windowsScratchSecurity(t *testing.T, path string) *windows.SECURITY_DESCRIPTOR {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return sd
}

func TestPrivateSnapshotScratchAcceptsNativeDACL(t *testing.T) {
	user := windowsScratchUser(t)
	for _, grants := range []string{"(A;OICI;FA;;;" + user + ")", "(A;OICI;FA;;;" + user + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"} {
		t.Run(grants, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(tempDir(t), "private")
			createWindowsScratchFixture(t, path, "O:"+user+"D:P"+grants)
			before := windowsScratchSecurity(t, path).String()
			if err := ensurePrivateDirectory(path); err != nil {
				t.Fatalf("private native directory refused: %v", err)
			}
			if after := windowsScratchSecurity(t, path).String(); after != before {
				t.Fatalf("existing ACL changed: %q -> %q", before, after)
			}
		})
	}
}

func TestPrivateSnapshotScratchRejectsUnsafeExistingDACL(t *testing.T) {
	user := windowsScratchUser(t)
	for _, grants := range []string{
		"P(A;OICI;FA;;;" + user + ")(A;OICI;FR;;;WD)",
		"P(A;OICI;FA;;;" + user + ")(A;OICIIO;FA;;;WD)",
		"NO_ACCESS_CONTROL",
	} {
		t.Run(grants, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(tempDir(t), "unsafe")
			createWindowsScratchFixture(t, path, "O:"+user+"D:"+grants)
			before := windowsScratchSecurity(t, path).String()
			if err := ensurePrivateDirectory(path); err == nil {
				t.Fatal("unsafe native directory accepted")
			}
			if after := windowsScratchSecurity(t, path).String(); after != before {
				t.Fatalf("refusal rewrote ACL: %q -> %q", before, after)
			}
		})
	}
}

func TestPrivateSnapshotScratchCreatesProtectedInheritableDACL(t *testing.T) {
	t.Parallel()
	user := windowsScratchUser(t)
	path := filepath.Join(tempDir(t), "new-private")
	if err := ensurePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	sd := windowsScratchSecurity(t, path)
	owner, _, err := sd.Owner()
	if err != nil || owner.String() != user {
		t.Fatalf("unexpected owner: %v %v", owner, err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("new directory inherits foreign ACLs: %v %v", control, err)
	}
	child := filepath.Join(path, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDirectory(child); err != nil {
		t.Fatalf("child did not inherit private ACL: %v; security=%s", err, windowsScratchSecurity(t, child).String())
	}
	if err := os.WriteFile(filepath.Join(child, "proof"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateSnapshotScratchRejectsJunctionParentBeforeCreation(t *testing.T) {
	t.Parallel()
	root := tempDir(t)
	target, junction := filepath.Join(root, "target"), filepath.Join(root, "junction")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cmd.exe", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
		t.Fatalf("create native junction fixture: %v %s", err, out)
	}
	if err := ensurePrivateDirectory(filepath.Join(junction, "scratch")); err == nil {
		t.Fatal("junction parent accepted")
	}
	if _, err := os.Stat(filepath.Join(target, "scratch")); !os.IsNotExist(err) {
		resolved, resolveErr := filepath.EvalSymlinks(junction)
		info, _ := os.Lstat(junction)
		t.Fatalf("refused junction path created a directory: %v; resolved=%q error=%v info=%+v", err, resolved, resolveErr, info)
	}
}
