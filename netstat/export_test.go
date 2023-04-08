package netstat

var pd = newProcessData()

var (
	// Exported for testing
	ParseAddr      = parseAddr
	ParseIPv4      = parseIPv4
	ParseIPv6      = parseIPv6
	ParseSockTab   = pd.parseSockTab
	OpenFileStream = pd.openFileStream
)
