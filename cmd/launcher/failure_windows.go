//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Only Explorer's standalone console needs a pause. Existing terminals and
// redirected automation must return their exit code without waiting for input.
func pauseOnFailure() {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return
	}
	var processes [2]uint32
	getProcesses := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList")
	count, _, _ := getProcesses.Call(uintptr(unsafe.Pointer(&processes[0])), uintptr(len(processes)))
	if count != 1 {
		return
	}
	fmt.Fprintln(os.Stderr, "启动失败，详细信息见上方提示。按回车退出。")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
