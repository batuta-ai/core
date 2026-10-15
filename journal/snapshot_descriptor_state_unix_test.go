//go:build darwin || linux

package journal

import (
	"context"
	"os"
	"syscall"
	"testing"
)

func descriptorFlags(file *os.File) (int, error) {
	conn, err := file.SyscallConn()
	if err != nil {
		return 0, err
	}
	var flags int
	var syscallErr error
	err = conn.Control(func(fd uintptr) {
		value, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, uintptr(syscall.F_GETFL), 0)
		if errno != 0 {
			syscallErr = errno
			return
		}
		flags = int(value)
	})
	if err != nil {
		return 0, err
	}
	return flags, syscallErr
}

func TestReadSnapshotRejectsPipeWithoutChangingDescriptorFlags(t *testing.T) {
	t.Parallel()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close(); writer.Close() })

	readerBefore, err := descriptorFlags(reader)
	if err != nil {
		t.Fatal(err)
	}
	writerBefore, err := descriptorFlags(writer)
	if err != nil {
		t.Fatal(err)
	}
	if readerBefore&syscall.O_NONBLOCK == 0 || writerBefore&syscall.O_NONBLOCK == 0 {
		t.Fatalf("pipe initially lacks O_NONBLOCK: reader=%#x writer=%#x", readerBefore, writerBefore)
	}

	snapshot, err := ReadSnapshot(context.Background(), reader)
	if err == nil || len(snapshot.Records()) != 0 || snapshot.Complete() {
		t.Errorf("ReadSnapshot() = %#v, %v; want rejection with no data", snapshot, err)
	}
	readerAfter, err := descriptorFlags(reader)
	if err != nil {
		t.Fatal(err)
	}
	writerAfter, err := descriptorFlags(writer)
	if err != nil {
		t.Fatal(err)
	}
	if readerAfter != readerBefore || writerAfter != writerBefore {
		t.Errorf("pipe flags changed: reader %#x -> %#x, writer %#x -> %#x", readerBefore, readerAfter, writerBefore, writerAfter)
	}

	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatalf("borrowed pipe writer unusable: %v", err)
	}
	var data [1]byte
	if _, err := reader.Read(data[:]); err != nil || data[0] != 'x' {
		t.Errorf("borrowed pipe reader unusable: data=%q err=%v", data, err)
	}
}
