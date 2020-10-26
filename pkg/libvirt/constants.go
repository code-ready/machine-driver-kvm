package libvirt

const (
	DriverName    = "libvirt"
	DriverVersion = "0.12.10"

	connectionString = "qemu:///system"
	dnsmasqStatus    = "/var/lib/libvirt/dnsmasq/%s.status"
	DefaultMemory    = 8096
	DefaultCPUs      = 4
	DefaultNetwork   = "crc"
	DefaultPool      = "crc"
	DefaultCacheMode = "default"
	DefaultIOMode    = "threads"
	DefaultSSHUser   = "core"
	DefaultSSHPort   = 22
)
