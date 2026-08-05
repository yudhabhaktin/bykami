//go:build windows

package printer

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg" // the format compose writes
	_ "image/png"  // and the one a hand-made sheet is most likely to be
	"os"
	"path/filepath"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func spoolerSupported() error { return nil }

// The Win32 surface this backend needs, resolved by name rather than linked.
//
// No cgo: the booth binary is cross-compiled with GOOS=windows from whatever
// machine cut the release, and a C toolchain in that path would be a build that
// only one person can make. It is the same constraint that decided the download
// page ships GIF rather than MP4.
var (
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	winspool = windows.NewLazySystemDLL("winspool.drv")

	procCreateDC          = gdi32.NewProc("CreateDCW")
	procDeleteDC          = gdi32.NewProc("DeleteDC")
	procStartDoc          = gdi32.NewProc("StartDocW")
	procEndDoc            = gdi32.NewProc("EndDoc")
	procAbortDoc          = gdi32.NewProc("AbortDoc")
	procStartPage         = gdi32.NewProc("StartPage")
	procEndPage           = gdi32.NewProc("EndPage")
	procGetDeviceCaps     = gdi32.NewProc("GetDeviceCaps")
	procSetStretchBltMode = gdi32.NewProc("SetStretchBltMode")
	procSetBrushOrgEx     = gdi32.NewProc("SetBrushOrgEx")
	procStretchDIBits     = gdi32.NewProc("StretchDIBits")

	procOpenPrinter  = winspool.NewProc("OpenPrinterW")
	procClosePrinter = winspool.NewProc("ClosePrinter")
	procGetJob       = winspool.NewProc("GetJobW")
	procSetJob       = winspool.NewProc("SetJobW")
)

// GetDeviceCaps indices.
const (
	capHorzRes         = 8
	capVertRes         = 10
	capLogPixelsX      = 88
	capLogPixelsY      = 90
	capPhysicalWidth   = 110
	capPhysicalHeight  = 111
	capPhysicalOffsetX = 112
	capPhysicalOffsetY = 113
)

const (
	stretchHalftone = 4
	dibRGBColors    = 0
	srcCopy         = 0x00CC0020
	gdiError        = 0xFFFFFFFF

	jobControlDelete = 5
)

type docInfoW struct {
	Size     int32
	DocName  *uint16
	Output   *uint16
	Datatype *uint16
	Type     uint32
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type jobInfo1W struct {
	JobID        uint32
	PrinterName  *uint16
	MachineName  *uint16
	UserName     *uint16
	Document     *uint16
	Datatype     *uint16
	StatusText   *uint16
	Status       uint32
	Priority     uint32
	Position     uint32
	TotalPages   uint32
	PagesPrinted uint32
	Submitted    windows.Systemtime
}

// spool draws the sheet onto the queue and then waits for the machine.
func (s *Spooler) spool(ctx context.Context, j spoolJob) error {
	img, err := readSheet(j.image)
	if err != nil {
		return err
	}
	bits, header := newDIB(img)

	hdc, err := openQueueDC(j.queue)
	if err != nil {
		return err
	}
	defer procDeleteDC.Call(hdc)

	dest, err := s.pageRect(hdc, j)
	if err != nil {
		return err
	}

	jobID, err := s.draw(hdc, j, dest, bits, &header)
	if err != nil {
		return err
	}
	return s.wait(ctx, j, jobID)
}

// openQueueDC opens a device context on a Windows print queue. The caller
// deletes it.
func openQueueDC(name string) (uintptr, error) {
	queue, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, fmt.Errorf("printer: queue name %q: %w", name, err)
	}
	driver, err := windows.UTF16PtrFromString("WINSPOOL")
	if err != nil {
		return 0, err
	}

	hdc, _, callErr := procCreateDC.Call(uintptr(unsafe.Pointer(driver)), uintptr(unsafe.Pointer(queue)), 0, 0)
	runtime.KeepAlive(driver)
	runtime.KeepAlive(queue)
	if hdc == 0 {
		return 0, fmt.Errorf("printer: Windows has no print queue called %q — check the name against Settings > Printers, and mind that it is the queue's name and not the model's: %s", name, why(callErr))
	}
	return hdc, nil
}

// probeQueue proves at startup that a configured queue is one this machine
// actually has, and throws the handle away. See NewSpooler for why a queue that
// is absent is fatal and a printer that is switched off is not.
func probeQueue(name string) error {
	hdc, err := openQueueDC(name)
	if err != nil {
		return err
	}
	procDeleteDC.Call(hdc)
	return nil
}

// rect is where on the sheet the image goes, in the device's own units.
type rect struct{ x, y, w, h int }

// pageRect works out the whole physical sheet, and checks it is the sheet this
// job is for.
//
// Edge to edge rather than inside the printable area: these prints have no
// margin by design, and the physical offset is what turns "the top-left of the
// paper" into the coordinate GDI wants.
func (s *Spooler) pageRect(hdc uintptr, j spoolJob) (rect, error) {
	var (
		physW = deviceCap(hdc, capPhysicalWidth)
		physH = deviceCap(hdc, capPhysicalHeight)
		offX  = deviceCap(hdc, capPhysicalOffsetX)
		offY  = deviceCap(hdc, capPhysicalOffsetY)
		dpiX  = deviceCap(hdc, capLogPixelsX)
		dpiY  = deviceCap(hdc, capLogPixelsY)
	)

	if physW <= 0 || physH <= 0 || dpiX <= 0 || dpiY <= 0 {
		s.log.Warn("printer: the driver reports no physical page, so the layout cannot be checked against the queue",
			"queue", j.queue, "layout", j.spec.Layout)
	} else if err := pageFits(j.spec, physW, physH, dpiX, dpiY); err != nil {
		return rect{}, err
	}

	if physW > 0 && physH > 0 {
		return rect{x: -offX, y: -offY, w: physW, h: physH}, nil
	}

	// Every driver reports the printable area even when it reports no physical
	// page. Falling back to it gives up the bleed rather than the print.
	w, h := deviceCap(hdc, capHorzRes), deviceCap(hdc, capVertRes)
	if w <= 0 || h <= 0 {
		return rect{}, fmt.Errorf("printer: queue %q reports no printable area at all", j.queue)
	}
	return rect{w: w, h: h}, nil
}

// draw sends the document and returns the spooler's job id.
func (s *Spooler) draw(hdc uintptr, j spoolJob, dest rect, bits []byte, header *bitmapInfoHeader) (uint32, error) {
	name, err := windows.UTF16PtrFromString(j.name)
	if err != nil {
		return 0, err
	}
	doc := docInfoW{DocName: name}
	doc.Size = int32(unsafe.Sizeof(doc))

	id, _, callErr := procStartDoc.Call(hdc, uintptr(unsafe.Pointer(&doc)))
	runtime.KeepAlive(name)
	runtime.KeepAlive(&doc)
	if int32(id) <= 0 {
		return 0, fmt.Errorf("printer: the spooler refused the document on %q: %s", j.queue, why(callErr))
	}

	// Anything below that returns without EndDoc has left a half-written
	// document in the queue, and a half-written document still feeds paper.
	done := false
	defer func() {
		if !done {
			procAbortDoc.Call(hdc)
		}
	}()

	// Halftone is the quality mode, and SetBrushOrgEx is not optional with it —
	// Windows requires the brush origin be set whenever this mode is selected.
	procSetStretchBltMode.Call(hdc, stretchHalftone)
	procSetBrushOrgEx.Call(hdc, 0, 0, 0)

	for page := range j.pages {
		if r, _, err := procStartPage.Call(hdc); int32(r) <= 0 {
			return 0, fmt.Errorf("printer: starting sheet %d of %d on %q: %s", page+1, j.pages, j.queue, why(err))
		}

		r, _, err := procStretchDIBits.Call(hdc,
			uintptr(dest.x), uintptr(dest.y), uintptr(dest.w), uintptr(dest.h),
			0, 0, uintptr(header.Width), uintptr(header.Height),
			uintptr(unsafe.Pointer(&bits[0])), uintptr(unsafe.Pointer(header)),
			dibRGBColors, srcCopy)
		runtime.KeepAlive(bits)
		runtime.KeepAlive(header)

		// Zero scan lines is treated as a failure alongside GDI_ERROR. It means
		// a blank sheet, and this package would rather raise a false alarm than
		// hand somebody a blank print and call it done — the same trade
		// internal/shutter makes when it reads the reply body.
		if uint32(r) == gdiError || r == 0 {
			return 0, fmt.Errorf("printer: the driver drew nothing on sheet %d of %d on %q: %s", page+1, j.pages, j.queue, why(err))
		}

		if r, _, err := procEndPage.Call(hdc); int32(r) <= 0 {
			return 0, fmt.Errorf("printer: finishing sheet %d of %d on %q: %s", page+1, j.pages, j.queue, why(err))
		}
	}

	if r, _, err := procEndDoc.Call(hdc); int32(r) <= 0 {
		return 0, fmt.Errorf("printer: closing the document on %q: %s", j.queue, why(err))
	}
	done = true

	s.log.Info("printer: spooled", "queue", j.queue, "spool_job", uint32(id), "sheets", j.pages, "layout", j.spec.Layout)
	return uint32(id), nil
}

// wait blocks until the spooler is finished with the document.
//
// This is the half of the backend that justifies the package. Handing bytes to
// the spooler is where window.print() stops; everything a booth needs to know —
// whether the sheet came out, whether the roll ran out, whether anybody is
// coming to fix it — is only knowable from here.
func (s *Spooler) wait(ctx context.Context, j spoolJob, jobID uint32) error {
	name, err := windows.UTF16PtrFromString(j.queue)
	if err != nil {
		return err
	}

	var h windows.Handle
	r, _, callErr := procOpenPrinter.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&h)), 0)
	runtime.KeepAlive(name)
	if r == 0 {
		// The document is spooled and will very likely print. Reporting that as
		// success would be a guess, so it is reported as a failure that says so
		// — the same wording failInterrupted uses, and for the same reason.
		return fmt.Errorf("printer: spooled to %q but cannot watch the job, so whether the sheet came out is unknown; reprint if it did not: %s", j.queue, why(callErr))
	}
	defer procClosePrinter.Call(uintptr(h))

	deadline := time.Now().Add(j.budget)
	ticker := time.NewTicker(j.poll)
	defer ticker.Stop()

	said := ""
	for {
		status, text, present, err := spoolerJobStatus(h, jobID)
		if err != nil {
			return err
		}

		switch {
		case !present:
			// A finished job disappears, unless the queue is keeping printed
			// documents. Gone is the ordinary way a print ends.
			return nil
		case status&(jobStatusPrinted|jobStatusComplete) != 0:
			return nil
		case status&jobStatusError != 0:
			return s.giveUp(h, jobID, fmt.Errorf("printer: %s", describeStatus(status, text)))
		case status&(jobStatusDeleted|jobStatusDeleting) != 0:
			// Somebody cancelled it in Windows. Not ours to cancel again.
			return fmt.Errorf("printer: the job was cancelled in the Windows queue")
		}

		if why := stalled(status); why != "" && why != said {
			said = why
			s.log.Warn("printer: waiting on the machine", "spool_job", jobID, "why", why, "giving_up_in", time.Until(deadline).Round(time.Second))
		}

		if !time.Now().Before(deadline) {
			return s.giveUp(h, jobID, fmt.Errorf("printer: gave up after %s: %s", j.budget, describeStatus(status, text)))
		}

		select {
		case <-ctx.Done():
			return s.giveUp(h, jobID, ctx.Err())
		case <-ticker.C:
		}
	}
}

// giveUp cancels the document in Windows before reporting the failure.
//
// Without this, a job the queue has already recorded as failed is still sitting
// in the spooler, and prints the moment somebody loads paper — by which time a
// human has reprinted it by hand. One customer, two sheets, and a media ledger
// that no longer matches the roll.
func (s *Spooler) giveUp(h windows.Handle, jobID uint32, cause error) error {
	if r, _, err := procSetJob.Call(uintptr(h), uintptr(jobID), 0, 0, jobControlDelete); r == 0 {
		s.log.Error("printer: could not cancel the spool job, so it may still print later", "spool_job", jobID, "err", err)
	}
	return cause
}

// spoolerJobStatus reads one job. present is false once the spooler has
// forgotten it, which is how a completed print usually ends.
func spoolerJobStatus(h windows.Handle, jobID uint32) (status uint32, text string, present bool, err error) {
	buf := make([]byte, 1024)
	for range 2 {
		var needed uint32
		r, _, callErr := procGetJob.Call(uintptr(h), uintptr(jobID), 1,
			uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&needed)))
		if r != 0 {
			info := (*jobInfo1W)(unsafe.Pointer(&buf[0]))
			status, text = info.Status, windows.UTF16PtrToString(info.StatusText)
			runtime.KeepAlive(buf)
			return status, text, true, nil
		}

		switch {
		case errors.Is(callErr, windows.ERROR_INSUFFICIENT_BUFFER) && int(needed) > len(buf):
			buf = make([]byte, needed)
		case errors.Is(callErr, windows.ERROR_INVALID_PARAMETER), errors.Is(callErr, windows.ERROR_NOT_FOUND):
			return 0, "", false, nil
		default:
			return 0, "", false, fmt.Errorf("printer: reading spool job %d: %s", jobID, why(callErr))
		}
	}
	return 0, "", false, fmt.Errorf("printer: spool job %d: the spooler kept asking for a bigger buffer", jobID)
}

// why keeps a zero errno out of a log line.
//
// Every Win32 call hands back an errno whether or not it failed, so the naive
// wrapping produces "the driver drew nothing: The operation completed
// successfully" — a sentence nobody should have to read at 9pm at an event with
// a queue of people waiting, and the job's error column is exactly where it
// would end up.
func why(err error) string {
	if err == nil || errors.Is(err, windows.ERROR_SUCCESS) {
		return "the driver gave no reason"
	}
	return err.Error()
}

func deviceCap(hdc uintptr, index int) int {
	r, _, _ := procGetDeviceCaps.Call(hdc, uintptr(index))
	return int(int32(r))
}

func readSheet(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("printer: %w", err)
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("printer: %s is not an image this can print: %w", filepath.Base(path), err)
	}
	return img, nil
}

// newDIB converts the composed sheet into the bitmap GDI wants: 24-bit BGR,
// rows padded to four bytes, and bottom-up.
//
// Bottom-up — a positive height — rather than the top-down form, because
// top-down DIBs are the less travelled path through printer drivers and the
// cost of writing the rows backwards is one loop over an image that is about to
// spend twelve seconds in a dye-sub printer.
func newDIB(img image.Image) ([]byte, bitmapInfoHeader) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	// Always copied into a fresh RGBA at the origin. compose hands over a JPEG,
	// which decodes to YCbCr and would need converting anyway, and this drops
	// the question of what a non-zero Min would do to the indexing below.
	src := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)

	stride := (w*3 + 3) &^ 3
	buf := make([]byte, stride*h)
	for y := range h {
		row := buf[(h-1-y)*stride:]
		pix := src.Pix[y*src.Stride:]
		for x := range w {
			p, q := pix[x*4:], row[x*3:]
			q[0], q[1], q[2] = p[2], p[1], p[0]
		}
	}

	header := bitmapInfoHeader{
		Width:     int32(w),
		Height:    int32(h),
		Planes:    1,
		BitCount:  24,
		SizeImage: uint32(stride * h),
	}
	header.Size = uint32(unsafe.Sizeof(header))
	return buf, header
}
