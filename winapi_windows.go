//go:build windows

package main

import (
	"errors"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	user32                         = syscall.NewLazyDLL("user32.dll")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	procCreateMutexW               = kernel32.NewProc("CreateMutexW")
	procMessageBoxW                = user32.NewProc("MessageBoxW")
)

const (
	processQueryLimitedInformation = 0x1000
	createNoWindow                 = 0x08000000
	errorAlreadyExists             = 183

	mbOK          = 0x00000000
	mbOKCancel    = 0x00000001
	mbIconInfo    = 0x00000040
	mbIconWarning = 0x00000030
	mbIconError   = 0x00000010
	mbTopmost     = 0x00040000
	idOK          = 1
)

// Caminho completo de cada processo com esse nome de executavel. O nome sozinho nao
// basta: a Proton decide quem entra no tunel pelo caminho, e o Discord muda de pasta
// a cada atualizacao.
func processPaths(exeName string) ([]string, error) {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.CloseHandle(snap)

	var entry syscall.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	var paths []string
	for err = syscall.Process32First(snap, &entry); err == nil; err = syscall.Process32Next(snap, &entry) {
		if !strings.EqualFold(syscall.UTF16ToString(entry.ExeFile[:]), exeName) {
			continue
		}
		// Processo que morreu entre a foto e a consulta nao e erro, so nao conta.
		if path, err := imagePath(entry.ProcessID); err == nil {
			paths = append(paths, path)
		}
	}
	if !errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
		return paths, err
	}
	return paths, nil
}

func imagePath(pid uint32) (string, error) {
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, pid)
	if err != nil {
		return "", err
	}
	defer syscall.CloseHandle(handle)

	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	ok, _, callErr := procQueryFullProcessImageNameW.Call(uintptr(handle), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if ok == 0 {
		return "", callErr
	}
	return syscall.UTF16ToString(buf[:size]), nil
}

// Dois cliques seguidos no atalho rodariam duas copias editando a mesma configuracao
// e reiniciando a Proton uma em cima da outra.
func claimSingleInstance(name string) (bool, error) {
	ptr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return false, err
	}
	handle, _, callErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(ptr)))
	if handle == 0 {
		return false, callErr
	}
	// O handle fica aberto de proposito ate o processo sair: e ele que segura a vez.
	if errno, ok := callErr.(syscall.Errno); ok && errno == errorAlreadyExists {
		return false, nil
	}
	return true, nil
}

func messageBox(text string, flags uint32) int {
	title, _ := syscall.UTF16PtrFromString(appTitle)
	body, _ := syscall.UTF16PtrFromString(text)
	ret, _, _ := procMessageBoxW.Call(0, uintptr(unsafe.Pointer(body)), uintptr(unsafe.Pointer(title)), uintptr(flags|mbTopmost))
	return int(ret)
}

func hiddenProcess() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
