package config

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
)

func TestIsReadOnlyFilesystemMatchesOnlyEROFS(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "erofs-direct", err: syscall.EROFS, want: true},
		{name: "erofs-wrapped-patherror", err: &os.PathError{Op: "open", Path: "/x", Err: syscall.EROFS}, want: true},
		{name: "erofs-wrapped-fmt", err: fmt.Errorf("rename: %w", syscall.EROFS), want: true},
		{name: "eacces", err: syscall.EACCES, want: false},
		{name: "eperm", err: syscall.EPERM, want: false},
		{name: "os-errpermission", err: os.ErrPermission, want: false},
		{name: "permission-denied-string", err: errors.New("permission denied"), want: false},
		{name: "generic-error", err: errors.New("disk full"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsReadOnlyFilesystem(tc.err); got != tc.want {
				t.Fatalf("IsReadOnlyFilesystem(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
