package netns

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strconv"
	"sync"
	"syscall"

	"github.com/nberlee/go-netstat/common"
)

var (
	NetNSPath = "/var/run/netns"
)

func GetNetNSNames() (netNSNames []string, err error) {
	files, err := os.ReadDir(NetNSPath)
	if err != nil {
		return nil, err
	}

	for _, file := range files {
		if !file.IsDir() {
			netNSNames = append(netNSNames, file.Name())
		}
	}

	return netNSNames, nil
}

func GetNetNsPids(netNSNames []string) (pidNetNS *map[uint32]string) {
	inodes, err := getNetNsInodeFromBindMount(netNSNames)
	if err != nil {
		inodes, err = getNetNsInodeFromSymlink(netNSNames)
		if err != nil {
			return &map[uint32]string{}
		}
	}

	pidNetNS, err = getPidofNetNsFromProcInodes(inodes, netNSNames)
	if err != nil {
		return &map[uint32]string{}
	}

	return pidNetNS
}

func getNetNsInodeFromBindMount(netNSNames []string) (inodes []string, err error) {
	for _, netNSName := range netNSNames {
		sanitizedNetNSName := path.Base(netNSName)
		netNSPath := path.Join(NetNSPath, sanitizedNetNSName)

		f, err := os.Open(netNSPath)
		if err != nil {
			return nil, err
		}

		var stat syscall.Stat_t
		err = syscall.Fstat(int(f.Fd()), &stat)
		if err != nil {
			f.Close()
			return nil, err
		}

		inode := stat.Ino
		inodeStr := fmt.Sprintf("net:[%d]", inode)
		inodes = append(inodes, inodeStr)

		err = f.Close()
		if err != nil {
			return nil, err
		}
	}
	return inodes, nil
}

func getNetNsInodeFromSymlink(netNSNames []string) (inodes []string, err error) {
	for _, netNSName := range netNSNames {
		sanitizedNetNSName := path.Base(netNSName)
		symlinkPath := path.Join(NetNSPath, sanitizedNetNSName)

		fileInfo, err := os.Stat(symlinkPath)
		if err != nil {
			return nil, err
		}

		stat, ok := fileInfo.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, fmt.Errorf("failed to convert to syscall.Stat_t")
		}

		inode := stat.Ino
		inodeStr := fmt.Sprintf("net:[%d]", inode)
		inodes = append(inodes, inodeStr)
	}

	return inodes, nil
}

func getPidofNetNsFromProcInodes(inode []string, netNSNames []string) (*map[uint32]string, error) {
	var wg sync.WaitGroup
	var mu sync.Mutex

	pidNetNS := make(map[uint32]string)

	entries, err := os.ReadDir(common.ProcPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read proc directory: %w", err)
	}

	inodeToFind := make(map[string]int, len(inode))
	for i, f := range inode {
		inodeToFind[f] = i
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		pid, err := strconv.ParseUint(entry.Name(), 10, 32)
		if err != nil {
			continue
		}

		wg.Add(1)
		go func(pid uint32) {
			defer wg.Done()

			netNsPath := path.Join(common.ProcPath, strconv.FormatUint(uint64(pid), 10), "ns", "net")
			target, err := os.Readlink(netNsPath)
			if err != nil {
				return
			}

			mu.Lock()
			defer mu.Unlock()

			if i, ok := inodeToFind[target]; ok && len(netNSNames) > 0 {
				pidNetNS[pid] = netNSNames[i]
				delete(inodeToFind, target)

				if len(inodeToFind) == 0 {
					return
				}
			}
		}(uint32(pid))
	}

	wg.Wait()

	if len(pidNetNS) == 0 {
		return nil, errors.New("no matching file descriptors found")
	}

	return &pidNetNS, nil
}
