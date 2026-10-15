//go:build windows

package main

import (
	"errors"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	user32                         = syscall.NewLazyDLL("user32.dll")
	iphlpapi                       = syscall.NewLazyDLL("iphlpapi.dll")
	procGetExtendedTcpTable        = iphlpapi.NewProc("GetExtendedTcpTable")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	procCreateMutexW               = kernel32.NewProc("CreateMutexW")
	procWaitForSingleObject        = kernel32.NewProc("WaitForSingleObject")
	procReleaseMutex               = kernel32.NewProc("ReleaseMutex")
	procMessageBoxW                = user32.NewProc("MessageBoxW")
)

const (
	processQueryLimitedInformation = 0x1000
	createNoWindow                 = 0x08000000

	waitObject0   = 0x00000000
	waitAbandoned = 0x00000080
	waitTimeout   = 0x00000102

	mbOK          = 0x00000000
	mbOKCancel    = 0x00000001
	mbIconInfo    = 0x00000040
	mbIconWarning = 0x00000030
	mbIconError   = 0x00000010
	mbTopmost     = 0x00040000
	idOK          = 1

	afInet                  = 2
	tcpTableOwnerPidAll     = 5
	errorInsufficientBuffer = 122
)

type process struct {
	pid  uint32
	path string
}

// Caminho completo de cada processo com esse nome de executavel. O nome sozinho nao
// basta: a Proton decide quem entra no tunel pelo caminho, e o Discord muda de pasta
// a cada atualizacao.
func processPaths(exeName string) ([]string, error) {
	procs, err := processes(exeName)
	paths := make([]string, len(procs))
	for i, p := range procs {
		paths[i] = p.path
	}
	return paths, err
}

func processes(exeName string) ([]process, error) {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.CloseHandle(snap)

	var entry syscall.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	var procs []process
	for err = syscall.Process32First(snap, &entry); err == nil; err = syscall.Process32Next(snap, &entry) {
		if !strings.EqualFold(syscall.UTF16ToString(entry.ExeFile[:]), exeName) {
			continue
		}
		// Processo que morreu entre a foto e a consulta nao e erro, so nao conta.
		if path, err := imagePath(entry.ProcessID); err == nil {
			procs = append(procs, process{entry.ProcessID, path})
		}
	}
	if !errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
		return procs, err
	}
	return procs, nil
}

// Conexoes TCP IPv4 estabelecidas da maquina toda, com o processo dono de cada uma. Nao
// precisa de administrador. A tabela pode crescer entre a consulta do tamanho e a
// leitura, dai as tentativas.
func establishedTCP() ([]tcpConn, error) {
	size := uint32(64 << 10)
	for attempt := 0; attempt < 5; attempt++ {
		buf := make([]byte, size)
		ret, _, _ := procGetExtendedTcpTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, afInet, tcpTableOwnerPidAll, 0)
		switch ret {
		case 0:
			return parseTCPTable(buf), nil
		case errorInsufficientBuffer:
			continue
		}
		return nil, syscall.Errno(ret)
	}
	return nil, errors.New("a tabela de conexoes nao parou de crescer")
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

// namedMutex organiza a fila entre copias: duas trocando a lista ao mesmo tempo
// reiniciariam a Proton uma em cima da outra. O dono de um mutex no Windows e a
// thread, entao quem usa precisa ter chamado runtime.LockOSThread.
type namedMutex struct {
	handle uintptr
}

func openMutex(name string) (*namedMutex, error) {
	ptr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	handle, _, callErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(ptr)))
	if handle == 0 {
		return nil, callErr
	}
	return &namedMutex{handle}, nil
}

// Mutex abandonado, de uma copia que morreu segurando a vez, tambem conta como pego.
func (m *namedMutex) acquire(timeout time.Duration) (bool, error) {
	ret, _, callErr := procWaitForSingleObject.Call(m.handle, uintptr(timeout.Milliseconds()))
	switch ret {
	case waitObject0, waitAbandoned:
		return true, nil
	case waitTimeout:
		return false, nil
	}
	return false, callErr
}

func (m *namedMutex) release() {
	procReleaseMutex.Call(m.handle)
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
