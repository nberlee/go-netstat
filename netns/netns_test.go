package netns_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"testing"

	"github.com/nberlee/go-netstat/common"
	"github.com/nberlee/go-netstat/netns"
)

func TestGetNetNSNames(t *testing.T) {
	// 1. Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "netns_test")
	if err != nil {
		t.Fatalf("Failed to create temporary directory: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// 2. Create files and directories within the temporary directory
	items := []struct {
		name  string
		isDir bool
	}{
		{"netns1", false},
		{"netns2", false},
		{"not_netns", true},
	}

	for _, item := range items {
		path := filepath.Join(tmpDir, item.name)
		if item.isDir {
			err = os.Mkdir(path, 0755)
		} else {
			err = os.WriteFile(path, []byte{}, 0644)
		}
		if err != nil {
			t.Fatalf("Failed to create test item: %v", err)
		}
	}
	netns.NetNSPath = tmpDir
	// 3. Test the function with the temporary directory
	got, err := netns.GetNetNSNames()
	if err != nil {
		t.Fatalf("GetNetNSNames() returned error: %v", err)
	}

	want := []string{"netns1", "netns2"}
	if len(got) != len(want) {
		t.Fatalf("GetNetNSNames() returned wrong number of items: got %v, want %v", got, want)
	}

	for i := range got {
		if got[i] != want[i] {
			t.Errorf("GetNetNSNames() returned wrong item: got %v, want %v", got[i], want[i])
		}
	}

	// 4. Test error handling by providing an invalid path
	netns.NetNSPath = "invalid_path"
	_, err = netns.GetNetNSNames()
	if err == nil {
		t.Fatal("GetNetNSNames() should return an error for an invalid path")
	}

	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("GetNetNSNames() returned wrong error: got %v, want %v", err, fs.ErrNotExist)
	}
}

func TestGetPidofNetNsFromProcInodes(t *testing.T) {
	// Create a mock /proc filesystem
	tmpProcPath, err := createMockProcFs()
	if err != nil {
		t.Fatalf("Failed to create mock /proc directory: %v", err)
	}
	defer os.RemoveAll(tmpProcPath)
	common.ProcPath = tmpProcPath

	// Test scenarios
	tests := []struct {
		name       string
		inode      []string
		netNSNames []string
		expected   *map[uint32]string
		wantErr    bool
		errText    string
	}{
		{
			name:       "matching_inodes",
			inode:      []string{"net:[1]", "net:[2]"},
			netNSNames: []string{"netns1", "netns2"},
			expected:   &map[uint32]string{1: "netns1", 2: "netns2"},
			wantErr:    false,
		},
		{
			name:       "empty_inodes",
			inode:      []string{},
			netNSNames: []string{"netns1", "netns2"},
			wantErr:    true,
			errText:    "no matching file descriptors found",
		},
		{
			name:       "non_matching_inodes",
			inode:      []string{"net:[4]", "net:[5]"},
			netNSNames: []string{"netns1", "netns2"},
			wantErr:    true,
			errText:    "no matching file descriptors found",
		},
		{
			name:       "empty_netNSNames",
			inode:      []string{"net:[1]", "net:[2]"},
			netNSNames: []string{},
			wantErr:    true,
			errText:    "no matching file descriptors found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test the function with the temporary /proc directory and the inodes
			pidNetNS, err := netns.GetPidofNetNsFromProcInodes(tt.inode, tt.netNSNames)

			// Check if an error is returned when expected
			if (err != nil) != tt.wantErr {
				t.Fatalf("getPidofNetNsFromProcInodes() returned unexpected error status: got %v, want %v", err, tt.wantErr)
			}

			if tt.wantErr {
				if err.Error() != tt.errText {
					t.Errorf("getPidofNetNsFromProcInodes() returned wrong error: got %v, want %v", err.Error(), tt.errText)
				}
				return
			}

			// Check if the returned PIDs match the expected values
			if len(*pidNetNS) != len(*tt.expected) {
				t.Fatalf("getPidofNetNsFromProcInodes() returned wrong number of items: got %v, want %v", *pidNetNS, *tt.expected)
			}

			for pid, netNSName := range *pidNetNS {
				if netNSName != (*tt.expected)[pid] {
					t.Errorf("getPidofNetNsFromNsFromProcInodes() returned wrong item: got %v, want %v", netNSName, (*tt.expected)[pid])
				}
			}
		})
	}
	// Test with an invalid temporary /proc directory
	t.Run("invalid_proc_path", func(t *testing.T) {
		common.ProcPath = path.Join(tmpProcPath, "nonexistent")
		_, err := netns.GetPidofNetNsFromProcInodes([]string{"net:[1]", "net:[2]"}, []string{"netns1", "netns2"})
		if err == nil {
			t.Fatal("getPidofNetNsFromProcInodes() should return an error for an invalid /proc path")
		}
	})
}

func TestGetNetNsInodeFromSymlink(t *testing.T) {
	// Create a temporary directory to store the mocked symlinks
	tmpNetNSPath, err := os.MkdirTemp("", "mock_netns_")
	if err != nil {
		t.Fatalf("Failed to create temporary directory: %v", err)
	}
	defer os.RemoveAll(tmpNetNSPath)

	// Create mocked symlinks and collect their inode values
	mockNetNSNames := []string{"netns1", "netns2"}
	expectedInodes := make([]string, len(mockNetNSNames))
	for i, netNSName := range mockNetNSNames {
		symlinkPath := path.Join(tmpNetNSPath, netNSName)
		err = os.Symlink("/proc/self/ns/net", symlinkPath)
		if err != nil {
			t.Fatalf("Failed to create mock symlink: %v", err)
		}

		fileInfo, err := os.Stat("/proc/self/ns/net")
		if err != nil {
			t.Fatalf("Failed to stat symlink: %v", err)
		}

		stat, ok := fileInfo.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatalf("Failed to convert to syscall.Stat_t")
		}

		inode := stat.Ino
		inodeStr := fmt.Sprintf("net:[%d]", inode)
		expectedInodes[i] = inodeStr
	}

	// Test scenarios
	tests := []struct {
		name       string
		netNSNames []string
		expected   []string
		wantErr    bool
		errText    string
	}{
		{
			name:       "existing_netns_names",
			netNSNames: mockNetNSNames,
			expected:   expectedInodes,
			wantErr:    false,
		},
		{
			name:       "non_existing_netns_name",
			netNSNames: []string{"nonexistent"},
			wantErr:    true,
			errText:    "stat " + path.Join(tmpNetNSPath, "nonexistent") + ": no such file or directory",
		},
		{
			name:       "empty_netns_names",
			netNSNames: []string{},
			expected:   nil,
			wantErr:    false,
		},
		{
			name:       "path exploits",
			netNSNames: []string{"../netns1", "../netns2"},
			expected:   expectedInodes,
			wantErr:    false,
		},
	}

	// Temporarily replace the NetNSPath global variable
	oldNetNSPath := netns.NetNSPath
	netns.NetNSPath = tmpNetNSPath
	defer func() { netns.NetNSPath = oldNetNSPath }()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inodes, err := netns.GetNetNsInodeFromSymlink(tt.netNSNames)

			if (err != nil) != tt.wantErr {
				t.Fatalf("GetNetNsInodeFromSymlink() returned unexpected error status: got %v, want %v", err, tt.wantErr)
			}

			if tt.wantErr {
				if err.Error() != tt.errText {
					t.Errorf("GetNetNsInodeFromSymlink() returned wrong error: got %v, want %v", err.Error(), tt.errText)
				}
				return
			}

			// Check if the returned inodes match the expected values
			if !reflect.DeepEqual(inodes, tt.expected) {
				t.Errorf("GetNetNsInodeFromSymlink() returned unexpected values: got %v, want %v", inodes, tt.expected)
			}
		})
	}
}

func createMockProcFs() (string, error) {
	tmpProcPath, err := os.MkdirTemp("", "proc_test")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary proc directory: %w", err)
	}

	pids := []uint32{1, 2, 3}
	for _, pid := range pids {
		pidPath := path.Join(tmpProcPath, strconv.FormatUint(uint64(pid), 10))
		err = os.Mkdir(pidPath, 0755)
		if err != nil {
			return "", fmt.Errorf("failed to create pid directory: %w", err)
		}

		nsPath := path.Join(pidPath, "ns")
		err = os.Mkdir(nsPath, 0755)
		if err != nil {
			return "", fmt.Errorf("failed to create ns directory: %w", err)
		}

		netNsPath := path.Join(nsPath, "net")
		err = os.Symlink(fmt.Sprintf("net:[%d]", pid), netNsPath)
		if err != nil {
			return "", fmt.Errorf("failed to create net namespace symlink: %w", err)
		}
	}

	return tmpProcPath, nil
}
