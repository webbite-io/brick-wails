//go:build windows

package main

import (
	"errors"
	"fmt"
	"log"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// startupTaskID is the TaskId of the windows.startupTask extension in
// build/windows/msix/app_manifest.xml; keep the two in step.
const startupTaskID = "BrickStartup"

// The StartupTaskState values (Windows.ApplicationModel).
const (
	startupDisabled         = 0
	startupDisabledByUser   = 1
	startupEnabled          = 2
	startupDisabledByPolicy = 3
	startupEnabledByPolicy  = 4
)

// startAtLogin starts Brick at login through the package's startup task
// (Windows.ApplicationModel.StartupTask), which Windows lists under Settings →
// Apps → Startup and on Task Manager's Startup tab, where the user can turn it
// off too. The Run key and the Startup folder aren't options: an MSIX app's
// writes to both are redirected into its own package store, where Windows
// never looks. The state is read from Windows rather than kept here, so a
// switch made there shows on the next one. The tray menu and the setup wizard
// both switch it, and both go through Set so the tray's check follows either.
type startAtLogin struct {
	logger *log.Logger

	// mu makes a switch and the check that follows it one step, so two
	// switches can't leave the check showing the earlier one.
	mu   sync.Mutex
	item *application.MenuItem
}

// newStartAtLogin returns nil outside an MSIX package, which leaves both the
// tray item and the wizard's checkbox out: only a packaged app has a startup
// task, so a bare .exe (a dev build) has nothing to switch. The entry path is
// Linux's (ensureDesktopEntry), always "" here.
func newStartAtLogin(_ string, logger *log.Logger) *startAtLogin {
	if !runningPackaged() {
		return nil
	}
	if _, err := startupTaskState(); err != nil {
		logger.Printf("start at login: %v", err)
		return nil
	}
	return &startAtLogin{logger: logger}
}

// Supported reports whether Brick can start itself at login here.
func (s *startAtLogin) Supported() bool { return s != nil }

// Preference is what the setup wizard's checkbox starts as: the current
// setting, or on when it has never been set — unless the user or a policy has
// already turned it off in Windows, which a fresh install shouldn't override.
func (s *startAtLogin) Preference() bool {
	if s == nil {
		return false
	}
	state, err := startupTaskState()
	if err != nil {
		return false
	}
	switch state {
	case startupEnabled, startupEnabledByPolicy:
		return true
	case startupDisabledByUser, startupDisabledByPolicy:
		return false
	}
	return !startAtLoginChosen()
}

// Set turns starting at login on or off and updates the tray's check.
func (s *startAtLogin) Set(on bool) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setLocked(on)
}

func (s *startAtLogin) setLocked(on bool) error {
	setStartAtLoginChosen()
	err := setStartupTask(on)
	if err != nil {
		s.logger.Printf("start at login: %v", err)
	}
	s.applyCheckLocked()
	return err
}

// addMenuItem adds the "Start at Login" toggle to the tray menu: a plain
// checkbox item, since Windows menus draw the check in the same place for
// every item.
func (s *startAtLogin) addMenuItem(menu *application.Menu) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.item = menu.AddCheckbox("Start at Login", false)
	s.applyCheckLocked()
	s.mu.Unlock()

	// Wails has already flipped the check by the time this runs; the switch
	// goes by what Windows says instead, and the check is put right after.
	s.item.OnClick(func(*application.Context) {
		s.mu.Lock()
		defer s.mu.Unlock()
		state, err := startupTaskState()
		if err != nil {
			s.logger.Printf("start at login: %v", err)
			s.applyCheckLocked()
			return
		}
		_ = s.setLocked(state != startupEnabled && state != startupEnabledByPolicy)
	})
}

// applyCheckLocked shows the check while starting at login is on, and greys
// the item out while a policy decides it instead of the user.
func (s *startAtLogin) applyCheckLocked() {
	if s.item == nil {
		return
	}
	state, err := startupTaskState()
	if err != nil {
		return
	}
	s.item.SetChecked(state == startupEnabled || state == startupEnabledByPolicy)
	s.item.SetEnabled(state != startupDisabledByPolicy && state != startupEnabledByPolicy)
}

// setStartupTask turns the startup task on or off. One the user switched off
// in Windows can't be turned back on by the app, only by the user, so that
// opens Settings' Startup page for them.
func setStartupTask(on bool) error {
	return withStartupTask(func(task unsafe.Pointer) error {
		var state int32
		if err := comCall(task, iStartupTaskGetState, uintptr(unsafe.Pointer(&state))); err != nil {
			return fmt.Errorf("reading startup task state: %w", err)
		}
		if !on {
			if state == startupEnabled {
				if err := comCall(task, iStartupTaskDisable); err != nil {
					return fmt.Errorf("disabling startup task: %w", err)
				}
			}
			return nil
		}
		switch state {
		case startupEnabled, startupEnabledByPolicy:
			return nil
		case startupDisabledByPolicy:
			return errors.New("starting at login is turned off by your organization's policy")
		case startupDisabledByUser:
			return openStartupSettings()
		}
		var op unsafe.Pointer
		if err := comCall(task, iStartupTaskRequestEnableAsync, uintptr(unsafe.Pointer(&op))); err != nil {
			return fmt.Errorf("enabling startup task: %w", err)
		}
		defer comRelease(op)
		var newState int32
		if err := awaitAsync(op, uintptr(unsafe.Pointer(&newState))); err != nil {
			return fmt.Errorf("enabling startup task: %w", err)
		}
		if newState == startupDisabledByUser {
			return openStartupSettings()
		}
		return nil
	})
}

// startupTaskState reads the startup task's StartupTaskState.
func startupTaskState() (int32, error) {
	var state int32
	err := withStartupTask(func(task unsafe.Pointer) error {
		return comCall(task, iStartupTaskGetState, uintptr(unsafe.Pointer(&state)))
	})
	return state, err
}

func openStartupSettings() error {
	verb, _ := windows.UTF16PtrFromString("open")
	uri, _ := windows.UTF16PtrFromString("ms-settings:startupapps")
	return windows.ShellExecute(0, verb, uri, nil, nil, windows.SW_SHOWNORMAL)
}

// --- Whether the user has chosen ---

// The startup task's state alone can't say whether nobody has chosen yet: a
// task that was never enabled and one that was turned off both read as
// Disabled. So the first switch, from the tray or the setup wizard, leaves a
// mark in the registry. An MSIX app's HKCU writes land in its own package
// store, which is fine here: only the app reads it, and it goes away with the
// app.
const chosenKeyPath = `Software\Webbite\Brick`
const chosenValue = "StartAtLoginChosen"

func startAtLoginChosen() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, chosenKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetIntegerValue(chosenValue)
	return err == nil
}

func setStartAtLoginChosen() {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, chosenKeyPath, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	_ = k.SetDWordValue(chosenValue, 1)
}

// --- WinRT, without CGO ---
//
// StartupTask is a WinRT class, called here through its vtables so the app
// keeps cross-compiling with CGO_ENABLED=0. The slots below count from the
// start of each vtable: IUnknown takes 0-2, IInspectable 3-5.

var (
	combase                    = windows.NewLazySystemDLL("combase.dll")
	procRoGetActivationFactory = combase.NewProc("RoGetActivationFactory")
	procWindowsCreateString    = combase.NewProc("WindowsCreateString")
	procWindowsDeleteString    = combase.NewProc("WindowsDeleteString")

	procGetCurrentPackageFullName = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetCurrentPackageFullName")
)

var (
	iidIStartupTaskStatics = windows.GUID{Data1: 0xee5b60bd, Data2: 0xa148, Data3: 0x41a7, Data4: [8]byte{0xb2, 0x6e, 0xe8, 0xb8, 0x8a, 0x1e, 0x62, 0xf8}}
	iidIAsyncInfo          = windows.GUID{Data1: 0x00000036, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xc0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
)

const (
	iUnknownQueryInterface = 0
	iUnknownRelease        = 2

	iStartupTaskStaticsGetAsync = 7

	iStartupTaskRequestEnableAsync = 6
	iStartupTaskDisable            = 7
	iStartupTaskGetState           = 8

	iAsyncInfoGetStatus    = 7
	iAsyncInfoGetErrorCode = 8

	iAsyncOperationGetResults = 8
)

// The AsyncStatus values (Windows.Foundation).
const (
	asyncStarted   = 0
	asyncCompleted = 1
	asyncCanceled  = 2
)

const (
	sFalse                 = 1
	appmodelErrorNoPackage = 15700
	rpcEChangedMode        = 0x80010106
)

// asyncTimeout bounds the wait on a startup task call. They answer in
// milliseconds for a desktop app, which Windows never asks the user about.
const asyncTimeout = 10 * time.Second

// runningPackaged reports whether this process runs from an MSIX package.
func runningPackaged() bool {
	if procGetCurrentPackageFullName.Find() != nil {
		return false
	}
	var n uint32
	r, _, _ := procGetCurrentPackageFullName.Call(uintptr(unsafe.Pointer(&n)), 0)
	return r != appmodelErrorNoPackage
}

// withStartupTask runs fn with Brick's StartupTask. The calls happen on an OS
// thread of their own, joined to COM's multithreaded apartment for the
// duration, so they never touch the UI thread's apartment.
func withStartupTask(fn func(task unsafe.Pointer) error) error {
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		// A thread that's already in a single-threaded apartment stays
		// there; StartupTask can be called from either.
		switch err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); {
		case err == nil, errors.Is(err, syscall.Errno(sFalse)):
			defer windows.CoUninitialize()
		case errors.Is(err, syscall.Errno(rpcEChangedMode)):
		default:
			done <- fmt.Errorf("CoInitializeEx: %w", err)
			return
		}

		task, err := getStartupTask()
		if err != nil {
			done <- err
			return
		}
		defer comRelease(task)
		done <- fn(task)
	}()
	return <-done
}

func getStartupTask() (unsafe.Pointer, error) {
	class, err := newHString("Windows.ApplicationModel.StartupTask")
	if err != nil {
		return nil, err
	}
	defer deleteHString(class)
	var statics unsafe.Pointer
	if r, _, _ := procRoGetActivationFactory.Call(class, uintptr(unsafe.Pointer(&iidIStartupTaskStatics)), uintptr(unsafe.Pointer(&statics))); r != 0 {
		return nil, fmt.Errorf("RoGetActivationFactory: %w", hresult(r))
	}
	defer comRelease(statics)

	id, err := newHString(startupTaskID)
	if err != nil {
		return nil, err
	}
	defer deleteHString(id)
	var op unsafe.Pointer
	if err := comCall(statics, iStartupTaskStaticsGetAsync, id, uintptr(unsafe.Pointer(&op))); err != nil {
		return nil, fmt.Errorf("getting startup task %q: %w", startupTaskID, err)
	}
	defer comRelease(op)
	var task unsafe.Pointer
	if err := awaitAsync(op, uintptr(unsafe.Pointer(&task))); err != nil {
		return nil, fmt.Errorf("getting startup task %q: %w", startupTaskID, err)
	}
	return task, nil
}

// awaitAsync waits for the IAsyncOperation op to finish and writes its result
// to out. It polls the operation's IAsyncInfo rather than registering a
// completion handler, which would mean implementing a COM object in Go.
func awaitAsync(op unsafe.Pointer, out uintptr) error {
	var info unsafe.Pointer
	if err := comCall(op, iUnknownQueryInterface, uintptr(unsafe.Pointer(&iidIAsyncInfo)), uintptr(unsafe.Pointer(&info))); err != nil {
		return err
	}
	defer comRelease(info)

	deadline := time.Now().Add(asyncTimeout)
	for {
		var status int32
		if err := comCall(info, iAsyncInfoGetStatus, uintptr(unsafe.Pointer(&status))); err != nil {
			return err
		}
		switch status {
		case asyncStarted:
			if time.Now().After(deadline) {
				return errors.New("timed out")
			}
			time.Sleep(10 * time.Millisecond)
			continue
		case asyncCompleted:
			return comCall(op, iAsyncOperationGetResults, out)
		case asyncCanceled:
			return errors.New("canceled")
		default:
			var code uintptr
			if err := comCall(info, iAsyncInfoGetErrorCode, uintptr(unsafe.Pointer(&code))); err != nil {
				return err
			}
			return hresult(code)
		}
	}
}

// comCall calls the method in vtable slot of the COM object obj. Like
// syscall.SyscallN, it takes pointers converted to uintptr, kept alive and in
// place for the call by go:uintptrescapes.
//
//go:uintptrescapes
func comCall(obj unsafe.Pointer, slot int, args ...uintptr) error {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	if int32(r) < 0 {
		return hresult(r)
	}
	return nil
}

func comRelease(obj unsafe.Pointer) {
	if obj != nil {
		_ = comCall(obj, iUnknownRelease)
	}
}

func newHString(s string) (uintptr, error) {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return 0, err
	}
	var h uintptr
	if r, _, _ := procWindowsCreateString.Call(uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&h))); r != 0 {
		return 0, fmt.Errorf("WindowsCreateString: %w", hresult(r))
	}
	return h, nil
}

func deleteHString(h uintptr) { _, _, _ = procWindowsDeleteString.Call(h) }

// hresult is a failed HRESULT, worded by Windows where it knows the code.
type hresult uintptr

func (h hresult) Error() string {
	return fmt.Sprintf("%s (0x%08X)", syscall.Errno(uint32(h)).Error(), uint32(h))
}
