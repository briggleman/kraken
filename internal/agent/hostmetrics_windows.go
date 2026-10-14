//go:build windows

package agent

import (
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes     = kernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatus = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetTickCount64     = kernel32.NewProc("GetTickCount64")
)

// memoryStatusEx mirrors MEMORYSTATUSEX from sysinfoapi.h.
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

// hostReader reads host vitals through the Win32 API. Unlike the Linux reader
// there is no fixture seam: these are syscalls, so the parsing logic that would
// benefit from one doesn't exist here.
type hostReader struct {
	dataDir string
	exclude map[string]bool // KRAKEN_NET_EXCLUDE, lowercased
}

func newHostReader(dataDir string) *hostReader {
	return &hostReader{dataDir: dataDir, exclude: netExcludeFromEnv()}
}

func (r *hostReader) read() hostSnapshot {
	s := hostSnapshot{at: time.Now(), diskPath: r.dataDir}
	readWindowsCPU(&s)
	readWindowsMem(&s)
	readWindowsNet(&s, r.exclude)
	readWindowsUptime(&s)
	readDisk(r.dataDir, &s)
	return s
}

// readWindowsCPU reads system-wide CPU times. The kernel figure already includes
// idle, so total is kernel+user and busy is that minus idle.
func readWindowsCPU(s *hostSnapshot) {
	var idle, kernel, user windows.Filetime
	r1, _, err := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if r1 == 0 {
		warnHostMetricOnce("cpu", err)
		return
	}
	idleT, kernelT, userT := filetimeTicks(idle), filetimeTicks(kernel), filetimeTicks(user)
	total := kernelT + userT
	if total == 0 || idleT > total {
		return
	}
	s.cpuTotal = total
	s.cpuBusy = total - idleT
	s.cpuCores = runtime.NumCPU()
	s.cpuOK = true
}

func filetimeTicks(ft windows.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}

func readWindowsMem(s *hostSnapshot) {
	var m memoryStatusEx
	m.length = uint32(unsafe.Sizeof(m))
	r1, _, err := procGlobalMemoryStatus.Call(uintptr(unsafe.Pointer(&m)))
	if r1 == 0 {
		warnHostMetricOnce("memory", err)
		return
	}
	if m.totalPhys == 0 {
		return
	}
	const mb = 1024 * 1024
	s.memTotalMB = int64(m.totalPhys / mb)
	s.memUsedMB = int64((m.totalPhys - m.availPhys) / mb)
	s.memOK = true
}

// Interface classification constants from ifdef.h / netioapi.h.
const (
	ifOperStatusUp         = 1
	ifTypeSoftwareLoopback = 24
	// MIB_IF_ROW2.InterfaceAndOperStatusFlags: bit 0 is HardwareInterface, bit 1
	// FilterInterface.
	ifFlagHardwareInterface = 0x01
	ifFlagFilterInterface   = 0x02
)

// readWindowsNet totals bytes across the host's hardware interfaces.
//
// The HardwareInterface flag is what keeps this honest on a Docker Desktop or
// Hyper-V host: the "vEthernet (WSL)" and container NAT adapters are real
// interfaces with real counters, but every byte they carry also crosses the
// physical NIC, so counting both doubles the host's apparent throughput.
//
// It reads the whole interface table rather than net.Interfaces(). That one
// lists only adapters with an IP bound, and a NIC bound to a Hyper-V external
// switch has none (the host's address moves to the switch's vEthernet adapter),
// so the one interface that carries everything dropped out and the meter went
// dark (#432). See sumIfRows for which rows count.
func readWindowsNet(s *hostSnapshot, exclude map[string]bool) {
	rows, err := windowsIfRows()
	if err != nil {
		warnHostMetricOnce("network", err)
		return
	}
	s.netRxBytes, s.netTxBytes, s.netOK = sumIfRows(rows, exclude)
}

// windowsIfRows copies the interface table out of GetIfTable2Ex, freeing it
// before returning.
func windowsIfRows() ([]ifCounters, error) {
	var table *windows.MibIfTable2
	if err := windows.GetIfTable2Ex(windows.MibIfTableNormal, &table); err != nil {
		return nil, err
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	raw := unsafe.Slice(&table.Table[0], table.NumEntries)
	rows := make([]ifCounters, 0, len(raw))
	for i := range raw {
		r := &raw[i]
		rows = append(rows, ifCounters{
			alias:       windows.UTF16ToString(r.Alias[:]),
			description: windows.UTF16ToString(r.Description[:]),
			up:          r.OperStatus == ifOperStatusUp,
			loopback:    r.Type == ifTypeSoftwareLoopback,
			hardware:    r.InterfaceAndOperStatusFlags&ifFlagHardwareInterface != 0,
			filter:      r.InterfaceAndOperStatusFlags&ifFlagFilterInterface != 0,
			rx:          r.InOctets,
			tx:          r.OutOctets,
		})
	}
	return rows, nil
}

func readWindowsUptime(s *hostSnapshot) {
	ms, _, _ := procGetTickCount64.Call()
	if ms == 0 {
		return
	}
	s.uptime = time.Duration(ms) * time.Millisecond
}

// readDisk fills the disk fields from the volume holding dir. Used is total
// minus total-free (not minus caller-available), so a quota'd account doesn't
// make the volume look fuller than it is.
func readDisk(dir string, s *hostSnapshot) {
	dir = resolveDiskPath(dir)
	if dir == "" {
		return
	}
	s.diskPath = dir
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		warnHostMetricOnce("disk", err)
		return
	}
	var freeToCaller, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeToCaller, &total, &totalFree); err != nil {
		warnHostMetricOnce("disk", fmt.Errorf("%s: %w", dir, err))
		return
	}
	if total == 0 {
		return
	}
	const mb = 1024 * 1024
	s.diskTotalMB = int64(total / mb)
	s.diskUsedMB = int64((total - totalFree) / mb)
	s.diskOK = true
}
