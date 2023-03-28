# go-netstat
go-netstat is a Golang library that provides a fast and multi-threaded implementation of the netstat utility on Linux. It is designed to be feature-complete with the net-tools netstat implementation and supports network namespaces for containers. It was written with Talos Linux in mind.

## Installation
You can install go-netstat by running the following command:
```bash
go get github.com/nberlee/go-netstat/netstat
```

## Usage
Here's an example of how to use go-netstat:

```go
import (
    "context"
    "fmt"

    "github.com/nberlee/go-netstat/netstat"
)

func main() {
    ctx := context.Background()

    features := netstat.EnableFeatures{
        TCP:           true,
        TCP6:          true,
        UDP:           true,
        UDP6:          true,
        UDPLite:       true,
        UDPLite6:      true,
        Raw:           true,
        Raw6:          true,
        PID:           true,
        NoHostNetwork: false,
        AllNetNs:      true,
        NetNsName:     []string{},
        NetNsPids:     []uint32{},
    }

    fn := netstat.NoopFilter

    netstatResp, err := netstat.Netstat(ctx, features, fn)
    if err != nil {
        panic(err)
    }

    for _, entry := range netstatResp {
        fmt.Println(entry)
    }
}
```
The `features` struct specifies which types of sockets and connections to include in the result, while the `fn` function can be used to filter the results further.

## Contributing
We welcome contributions to go-netstat! If you have bug fixes, feature requests, or performance improvements, feel free to submit a pull request. We are especially interested in contributions that are close to the core functionality and that benefit Talos Linux netstat.

## Credits
The initial version of go-netstat was written by Cihangir Akturk, who contributed the functions ParseAddr, ParseIpv4, and ParseIpv6. Nico Berlee later rewrote and reworked every other function to make it faster by multi-threading and to add features like network namespaces, udplite+raw support and tests.

## License
go-netstat is released under the MIT License. See [LICENSE](https://github.com/nberlee/go-netstat/blob/main/LICENSE) for details.

## Types
### EnableFeatures
The EnableFeatures struct specifies which types of sockets and connections to include in the result.
```go
type EnableFeatures struct {
    TCP           bool
    TCP6          bool
    UDP           bool
    UDP6          bool
    UDPLite       bool
    UDPLite6      bool
    Raw           bool
    Raw6          bool
    PID           bool
    NoHostNetwork bool
    AllNetNs      bool
    NetNsName     []string
    NetNsPids     []uint32
}
```

### SockTabEntry
The SockTabEntry struct represents each line of the /proc/net/[tcp|udp] file.
```go
type SockTabEntry struct {
    Transport      string
    LocalEndpoint  *SockEndpoint
    RemoteEndpoint *SockEndpoint
    State          SkState
    TxQueue        uint64
    RxQueue        uint64
    Tr             TimerActive
    TimerWhen      uint64
    Retrnsmt       uint64
    UID            uint32
    Timeout        uint64
    Inode          uint64
    Ref            uint64
    Process        *common.Process
    NetNS          string
}
```

### SockEndpoint

The `SockEndpoint` struct represents an IP address and port.

```go
type SockEndpoint struct {
    IP   net.IP
    Port uint16
}
```
The `IP` field can be an IPv4 or IPv6 address.

## Filter Function
The fn function is used to filter the results of the Netstat function.
```go
type FilterFunc func(*SockTabEntry) bool
```

Here are some examples of filter functions:
```go
func NoopFilter(s *SockTabEntry) bool {
    return true
}

func ConnectedFilter(s *SockTabEntry) bool {
    return !s.RemoteEndpoint.IP.IsUnspecified() && s.RemoteEndpoint.Port != 0
}

func ListeningFilter(s *SockTabEntry) bool {
    return s.RemoteEndpoint.IP.IsUnspecified() && s.RemoteEndpoint.Port == 0
}
```

## Design decisions
* /proc/net/[tcp|udp|udplite|raw] are chosen over syscalls as accessing directly kernel data structures is can be faster than making syscalls with context switching overhead.
* `go-netstat` does not enter an network namespace, instead it gets the process id and reads the /proc/`processid`/net/[tcp|udp|udplite|raw]. This is safer and faster.

### Flows

#### Network namespace
Network Namespace Name -> Find in `/var/run/netns` -> Follow bindmount to /proc/`processid`/ns/net, get the symlink which is if for `net:[12345678]`. Search in /proc/`processid`/ns/net for the file descriptor with the same symlink.

#### Process ID + Name
To enrich netstat entries with the process id, the inode is matched against /proc/`processid`/fd/* symlinks.

