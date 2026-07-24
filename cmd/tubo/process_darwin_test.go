//go:build darwin

package main

import (
	"encoding/binary"
	"os"
	"os/exec"
	"reflect"
	"syscall"
	"testing"
	"time"
)

func TestDarwinProcessCommandLineCurrentProcess(t *testing.T) {
	args, ok := processCommandLine(os.Getpid())
	if !ok {
		t.Fatal("current process command line unavailable")
	}
	if len(args) == 0 || args[0] == "" {
		t.Fatalf("invalid command line: %#v", args)
	}
}

func TestDarwinPIDRunningRejectsZombie(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !pidRunning(cmd.Process.Pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("zombie pid %d reported running", cmd.Process.Pid)
}

func TestDarwinPIDRunningRejectsInvalidAndMissingPID(t *testing.T) {
	if pidRunning(0) || pidRunning(-1) {
		t.Fatal("non-positive PID reported running")
	}
	if pidRunning(1 << 30) {
		t.Fatal("missing PID reported running")
	}
}

func TestDarwinProcessCommandLineTreatsInaccessibleProcessAsUnavailable(t *testing.T) {
	args, ok := readDarwinCommandLine(123, func(string, ...int) ([]byte, error) {
		return nil, syscall.EPERM
	})
	if ok || args != nil {
		t.Fatalf("inaccessible command line = %#v, %v; want nil, false", args, ok)
	}
}

func TestParseDarwinProcArgs(t *testing.T) {
	raw := make([]byte, 4)
	binary.LittleEndian.PutUint32(raw, 3)
	raw = append(raw, []byte("/usr/bin/tubo\x00\x00tubo\x00connect\x00svc\x00ENV=value\x00")...)
	got, ok := parseDarwinProcArgs(raw)
	want := []string{"tubo", "connect", "svc"}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDarwinProcArgs() = %#v, %v; want %#v, true", got, ok, want)
	}
}

func TestParseDarwinProcArgsRejectsMalformedData(t *testing.T) {
	cases := [][]byte{
		nil,
		{0, 0, 0, 0},
		{2, 0, 0, 0, '/', 'x', 0, 0, 'x', 0},
	}
	for _, raw := range cases {
		if args, ok := parseDarwinProcArgs(raw); ok || args != nil {
			t.Fatalf("malformed data parsed as %#v", args)
		}
	}
}
