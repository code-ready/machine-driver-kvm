package libvirt

const (
	DriverName    = "libvirt"
	DriverVersion = "0.12.11"

	connectionString = "qemu:///system"
	DefaultMemory    = 8096
	DefaultCPUs      = 4
	DefaultNetwork   = "crc"
	DefaultPool      = "crc"
	DefaultCacheMode = "default"
	DefaultIOMode    = "threads"
	DefaultSSHUser   = "core"
	DefaultSSHPort   = 22
)
