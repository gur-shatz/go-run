//go:build !linux

package supervisor

import "syscall"

// dupFD duplicates oldfd onto newfd via dup2 on platforms (such as the macOS dev
// box) that do not provide dup3.
func dupFD(oldfd, newfd int) error {
	return syscall.Dup2(oldfd, newfd)
}
