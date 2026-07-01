//go:build linux

package supervisor

import "syscall"

// dupFD duplicates oldfd onto newfd. Linux drops the legacy dup2 syscall on
// newer architectures (arm64 has only dup3), so use dup3 with no flags, which is
// equivalent to dup2 here.
func dupFD(oldfd, newfd int) error {
	return syscall.Dup3(oldfd, newfd, 0)
}
