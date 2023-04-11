// Package netstat provides primitives for getting socket information on a
// Linux based operating system.
package netstat

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/nberlee/go-netstat/common"
	"github.com/nberlee/go-netstat/netns"
	"github.com/nberlee/go-netstat/processes"
)

const (
	pathTCPTab      = "net/tcp"
	pathTCP6Tab     = "net/tcp6"
	pathUDPTab      = "net/udp"
	pathUDP6Tab     = "net/udp6"
	pathUDPLiteTab  = "net/udplite"
	pathUDPLite6Tab = "net/udplite6"
	pathRawTab      = "net/raw"
	pathRaw6Tab     = "net/raw6"

	ipv4StrLen = 8
	ipv6StrLen = 32
)

// Socket states
const (
	Established SkState = iota + 1
	SynSent
	SynRecv
	FinWait1
	FinWait2
	TimeWait
	Close
	CloseWait
	LastAck
	Listen
	Closing
)

type processData struct {
	fdProcess map[uint64]*common.Process
	pidNetNS  map[uint32]string
}

func newProcessData() *processData {
	return &processData{
		fdProcess: make(map[uint64]*common.Process),
		pidNetNS:  make(map[uint32]string),
	}
}

var skStates = [...]string{
	"UNKNOWN",
	"ESTABLISHED",
	"SYN_SENT",
	"SYN_RECV",
	"FIN_WAIT1",
	"FIN_WAIT2",
	"TIME_WAIT",
	"CLOSE",
	"CLOSE_WAIT",
	"LAST_ACK",
	"LISTEN",
	"CLOSING",
}

// socket timer states
const (
	Off           TimerActive = 0x00
	On            TimerActive = 0x01
	KeepAlive     TimerActive = 0x02
	TimeWaitTimer TimerActive = 0x03 // Unsure how to call this
	ProbeTimer    TimerActive = 0x04 // Unsure how to call this
)

var TimerActives = [...]string{
	"Off",
	"On",
	"KeepAlive",
	"TimeWait",   // Unsure how to call this
	"ProbeTimer", // Unsure how to call this
}

func parseIPv4(s string) (net.IP, error) {
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return nil, err
	}
	ip := make(net.IP, net.IPv4len)
	binary.LittleEndian.PutUint32(ip, uint32(v))
	return ip, nil
}

func parseIPv6(s string) (net.IP, error) {
	ip := make(net.IP, net.IPv6len)
	const grpLen = 4
	i, j := 0, 4
	for len(s) != 0 {
		grp := s[0:8]
		u, err := strconv.ParseUint(grp, 16, 32)
		if err != nil {
			return nil, err
		}
		binary.LittleEndian.PutUint32(ip[i:j], uint32(u))
		i, j = i+grpLen, j+grpLen
		s = s[8:]
	}
	return ip, nil
}

func parseAddr(s string) (*SockEndpoint, error) {
	fields := strings.Split(s, ":")
	if len(fields) < 2 {
		return nil, fmt.Errorf("netstat: not enough fields: %v", s)
	}
	var ip net.IP
	var err error
	switch len(fields[0]) {
	case ipv4StrLen:
		ip, err = parseIPv4(fields[0])
	case ipv6StrLen:
		ip, err = parseIPv6(fields[0])
	default:
		err = fmt.Errorf("netstat: bad formatted string: %v", fields[0])
	}
	if err != nil {
		return nil, err
	}
	v, err := strconv.ParseUint(fields[1], 16, 16)
	if err != nil {
		return nil, err
	}
	return &SockEndpoint{IP: ip, Port: uint16(v)}, nil
}

func (pd *processData) parseSockTab(reader io.Reader, accept AcceptFn, transport string, podPid uint32) ([]SockTabEntry, error) {
	var netNsName string
	var ok bool

	if podPid > 0 {
		netNsName, ok = pd.pidNetNS[podPid]
		if !ok {
			netNsName = strconv.Itoa(int(podPid))
		}
	}
	scanner := bufio.NewScanner(reader)
	scanner.Scan()

	var sockTab []SockTabEntry
	for scanner.Scan() {
		line := scanner.Text()
		var entry SockTabEntry
		var localEndpoint, remoteEndpoint string
		var index int64
		_, err := fmt.Sscanf(line, "%d: %s %s %X %X:%X %d:%X %X %d %d %d %d %X",
			&index,
			&localEndpoint,
			&remoteEndpoint,
			&entry.State,
			&entry.TxQueue,
			&entry.RxQueue,
			&entry.Tr,
			&entry.TimerWhen,
			&entry.Retrnsmt,
			&entry.UID,
			&entry.Timeout,
			&entry.Inode,
			&entry.Ref,
			&entry.Pointer,
		)
		if err != nil {
			return nil, err
		}

		entry.LocalEndpoint, err = parseAddr(localEndpoint)
		if err != nil {
			return nil, err
		}
		entry.RemoteEndpoint, err = parseAddr(remoteEndpoint)
		if err != nil {
			return nil, err
		}
		entry.Transport = transport
		entry.NetNS = netNsName
		entry.Process = pd.fdProcess[entry.Inode]
		if accept(&entry) {
			sockTab = append(sockTab, entry)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return sockTab, nil
}

// Netstat - collect information about network port status
func Netstat(ctx context.Context, feature EnableFeatures, fn AcceptFn) ([]SockTabEntry, error) {
	var err error

	pd := newProcessData()

	pids, err := pd.mergePids(feature)
	if err != nil {
		return nil, err
	}

	files := procFiles(feature, pids)

	if feature.PID {
		pd.fdProcess, err = processes.GetProcessFDs(ctx)
		if err != nil {
			return nil, err
		}
	}
	// Create a channel for each file to receive its results.
	chs := make([]chan []SockTabEntry, len(files))
	for i := range chs {
		chs[i] = make(chan []SockTabEntry)
	}

	// Launch a goroutine for each file.
	for i, file := range files {
		go func(i int, file string) {
			select {
			case <-ctx.Done():
				// If the context is cancelled, send an empty slice and return.
				chs[i] <- []SockTabEntry{}
				return
			default:
				tabs, err := pd.openFileStream(file, fn)
				if err != nil {
					// Send an empty slice if there was an error.
					chs[i] <- []SockTabEntry{}
					return
				}
				chs[i] <- tabs
			}
		}(i, file)
	}

	// Collect the results from each channel in order and append them to combinedTabs.
	var combinedTabs []SockTabEntry
	for _, ch := range chs {
		select {
		case <-ctx.Done():
			// If the context is cancelled, return immediately with the current result.
			return combinedTabs, ctx.Err()
		case tabs := <-ch:
			combinedTabs = append(combinedTabs, tabs...)
		}
	}

	return combinedTabs, nil
}

func (pd *processData) mergePids(feature EnableFeatures) (pids []string, err error) {
	if feature.AllNetNs {
		netNsName, err := netns.GetNetNSNames()
		if err != nil {
			// PathError is expected when not running any container or CNI
			var pathError *fs.PathError
			if !errors.As(err, &pathError) {
				return nil, err
			}
		}

		feature.NetNsName = netNsName
		feature.NetNsPids = []uint32{}
	}

	if len(feature.NetNsName) > 0 {
		pd.pidNetNS = *netns.GetNetNsPids(feature.NetNsName)
	}

	hostNetNsIndex := 0
	if !feature.NoHostNetwork {
		hostNetNsIndex = 1
	}

	lengthPids := len(pd.pidNetNS) + len(feature.NetNsPids) + hostNetNsIndex
	pids = make([]string, lengthPids)

	if !feature.NoHostNetwork {
		pids[0] = ""
	}

	netNsNameIndex := 0

	for pid := range pd.pidNetNS {
		pids[netNsNameIndex+hostNetNsIndex] = strconv.Itoa(int(pid))
		netNsNameIndex++
	}

	for netNsPidIndex, pid := range feature.NetNsPids {
		pids[netNsPidIndex+netNsNameIndex+hostNetNsIndex] = strconv.Itoa(int(pid))
	}

	return pids, nil
}

func procFiles(feature EnableFeatures, pids []string) (files []string) {
	for _, pid := range pids {
		basePath := path.Join(common.ProcPath, pid)
		if feature.TCP {
			files = append(files, path.Join(basePath, pathTCPTab))
		}
		if feature.TCP6 {
			files = append(files, path.Join(basePath, pathTCP6Tab))
		}
		if feature.UDP {
			files = append(files, path.Join(basePath, pathUDPTab))
		}
		if feature.UDP6 {
			files = append(files, path.Join(basePath, pathUDP6Tab))
		}
		if feature.UDPLite {
			files = append(files, path.Join(basePath, pathUDPLiteTab))
		}
		if feature.UDPLite6 {
			files = append(files, path.Join(basePath, pathUDPLite6Tab))
		}
		if feature.Raw {
			files = append(files, path.Join(basePath, pathRawTab))
		}
		if feature.Raw6 {
			files = append(files, path.Join(basePath, pathRaw6Tab))
		}
	}
	return files
}

func (pd *processData) openFileStream(file string, fn AcceptFn) ([]SockTabEntry, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	_, transport := path.Split(file)
	podPid, _ := strconv.ParseUint(strings.Split(file, "/")[2], 10, 32)

	tabs, err := pd.parseSockTab(f, fn, transport, uint32(podPid))
	if err != nil {
		return nil, err
	}
	return tabs, nil
}
