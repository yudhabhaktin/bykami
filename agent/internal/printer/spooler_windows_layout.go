//go:build windows && amd64

package printer

import "unsafe"

// The Win32 structs in spooler_windows.go are laid out by hand, and a wrong
// offset there does not fail loudly — it reads a pointer out of the middle of
// another field, or hands the spooler a cbSize it rejects.
//
// None of it can be exercised by a test, because the machine that cuts the
// release is not the machine that runs the binary. So the sizes are asserted at
// compile time instead, which makes `GOOS=windows go build` a real check rather
// than a syntax check. Each pair fails if the struct is either too large or too
// small; the numbers are what the Windows SDK gives on x64.
const (
	_ = unsafe.Sizeof(docInfoW{}) - 40
	_ = 40 - unsafe.Sizeof(docInfoW{})

	_ = unsafe.Sizeof(bitmapInfoHeader{}) - 40
	_ = 40 - unsafe.Sizeof(bitmapInfoHeader{})

	_ = unsafe.Sizeof(jobInfo1W{}) - 96
	_ = 96 - unsafe.Sizeof(jobInfo1W{})
)
