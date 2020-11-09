package libvirt

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/libvirt/libvirt-go"
	libvirtxml "github.com/libvirt/libvirt-go-xml"

	// Machine-drivers
	libvirtdriver "github.com/code-ready/machine/drivers/libvirt"
	"github.com/code-ready/machine/libmachine/drivers"
	"github.com/code-ready/machine/libmachine/log"
	"github.com/code-ready/machine/libmachine/mcnflag"
	"github.com/code-ready/machine/libmachine/mcnutils"
	"github.com/code-ready/machine/libmachine/state"
)

type Driver struct {
	*libvirtdriver.Driver

	// Libvirt connection and state
	conn     *libvirt.Connect
	vm       *libvirt.Domain
	vmLoaded bool
}

func (d *Driver) GetCreateFlags() []mcnflag.Flag {
	return []mcnflag.Flag{
		mcnflag.IntFlag{
			Name:  "crc-libvirt-memory",
			Usage: "Size of memory for host in MB",
			Value: DefaultMemory,
		},
		mcnflag.IntFlag{
			Name:  "crc-libvirt-cpu-count",
			Usage: "Number of CPUs",
			Value: DefaultCPUs,
		},
		mcnflag.StringFlag{
			Name:  "crc-libvirt-network",
			Usage: "Name of network to connect to",
			Value: DefaultNetwork,
		},
		mcnflag.StringFlag{
			Name:  "crc-libvirt-cachemode",
			Usage: "Disk cache mode: default, none, writethrough, writeback, directsync, or unsafe",
			Value: DefaultCacheMode,
		},
		mcnflag.StringFlag{
			Name:  "crc-libvirt-iomode",
			Usage: "Disk IO mode: threads, native",
			Value: DefaultIOMode,
		},
		mcnflag.StringFlag{
			EnvVar: "CRC_LIBVIRT_SSHUSER",
			Name:   "crc-libvirt-sshuser",
			Usage:  "SSH username",
			Value:  DefaultSSHUser,
		},
	}
}

func (d *Driver) GetMachineName() string {
	return d.MachineName
}

func (d *Driver) GetSSHHostname() (string, error) {
	return d.GetIP()
}

func (d *Driver) GetSSHKeyPath() string {
	return d.SSHKeyPath
}

func (d *Driver) GetSSHPort() (int, error) {
	if d.SSHPort == 0 {
		d.SSHPort = DefaultSSHPort
	}

	return d.SSHPort, nil
}

func (d *Driver) GetSSHUsername() string {
	if d.SSHUser == "" {
		d.SSHUser = DefaultSSHUser
	}

	return d.SSHUser
}

func (d *Driver) DriverName() string {
	return DriverName
}

func (d *Driver) DriverVersion() string {
	return DriverVersion
}

func (d *Driver) SetConfigFromFlags(flags drivers.DriverOptions) error {
	log.Debugf("SetConfigFromFlags called")
	d.Memory = flags.Int("libvirt-memory")
	d.CPU = flags.Int("libvirt-cpu-count")
	d.Network = flags.String("libvirt-network")
	d.CacheMode = flags.String("libvirt-cachemode")
	d.IOMode = flags.String("libvirt-iomode")
	d.SSHPort = 22

	// CRC system bundle
	d.BundleName = flags.String("libvirt-bundlepath")
	return nil
}

func convertMBToKiB(sizeMb int) uint64 {
	return uint64(sizeMb) * 1000 * 1000 / 1024
}

func (d *Driver) setMemory(memorySize int) error {
	log.Debugf("Setting memory to %d MB", memorySize)
	if err := d.validateVMRef(); err != nil {
		return err
	}
	/* d.Memory is in MB, SetMemoryFlags expects kiB */
	err := d.vm.SetMemoryFlags(convertMBToKiB(memorySize), libvirt.DOMAIN_MEM_MAXIMUM)
	if err != nil {
		return err
	}
	err = d.vm.SetMemoryFlags(convertMBToKiB(memorySize), libvirt.DOMAIN_MEM_CONFIG)
	if err != nil {
		return err
	}

	d.Memory = memorySize

	return nil
}

func (d *Driver) setVcpus(cpus uint) error {
	log.Debugf("Setting vcpus to %d", cpus)
	if err := d.validateVMRef(); err != nil {
		return err
	}

	err := d.vm.SetVcpusFlags(cpus, libvirt.DOMAIN_VCPU_CONFIG|libvirt.DOMAIN_VCPU_MAXIMUM)
	if err != nil {
		return err
	}
	err = d.vm.SetVcpusFlags(cpus, libvirt.DOMAIN_VCPU_CONFIG)
	if err != nil {
		return err
	}

	d.CPU = int(cpus)

	return nil
}

func (d *Driver) UpdateConfigRaw(rawConfig []byte) error {
	var newDriver libvirtdriver.Driver
	err := json.Unmarshal(rawConfig, &newDriver)
	if err != nil {
		return err
	}
	// FIXME: not clear what the upper layers should do in case of partial errors?
	// is it the drivers implementation responsibility to keep a consistent internal state,
	// and should it return its (partial) new state when an error occurred?
	if newDriver.Memory != d.Memory {
		log.Debugf("Updating memory size to %d kiB", newDriver.Memory)
		err := d.setMemory(newDriver.Memory)
		if err != nil {
			log.Warnf("Failed to update memory: %v", err)
			return err
		}
	}
	if newDriver.CPU != d.CPU {
		log.Debugf("Updating vcpu count to %d", newDriver.CPU)
		err := d.setVcpus(uint(newDriver.CPU))
		if err != nil {
			log.Warnf("Failed to update CPU count: %v", err)
			return err
		}
	}
	if newDriver.SSHKeyPath != d.SSHKeyPath {
		log.Debugf("Updating SSH Key Path", d.SSHKeyPath, newDriver.SSHKeyPath)
	}
	diskCapacity, err := d.getVolCapacity()
	if err != nil {
		return err
	}

	if newDriver.DiskCapacity != 0 && newDriver.DiskCapacity != diskCapacity {
		log.Debugf("Updating disk image capacity to %d bytes", newDriver.DiskCapacity)
		err = d.resizeDiskImage(newDriver.DiskCapacity)
		if err != nil {
			log.Warnf("Failed to resize disk image: %v", err)
			return err
		}
	}

	*d.Driver = newDriver
	return nil
}

func (d *Driver) GetURL() (string, error) {
	return "", nil
}

func (d *Driver) getConn() (*libvirt.Connect, error) {
	if d.conn == nil {
		conn, err := libvirt.NewConnect(connectionString)
		if err != nil {
			log.Errorf("Failed to connect to libvirt: %s", err)
			return &libvirt.Connect{}, errors.New("Unable to connect to kvm driver, did you add yourself to the libvirtd group?")
		}
		d.conn = conn
	}
	return d.conn, nil
}

// Create, or verify the private network is properly configured
func (d *Driver) validateNetwork() error {
	if d.Network == "" {
		return nil
	}
	log.Debug("Validating network")
	conn, err := d.getConn()
	if err != nil {
		return err
	}
	network, err := conn.LookupNetworkByName(d.Network)
	if err != nil {
		return fmt.Errorf("Use 'crc setup' to define the network, %+v", err)
	}
	defer network.Free() // nolint:errcheck

	xmldoc, err := network.GetXMLDesc(0)
	if err != nil {
		return err
	}
	var nw libvirtxml.Network
	if err := nw.Unmarshal(xmldoc); err != nil {
		return err
	}

	if len(nw.IPs) != 1 {
		return fmt.Errorf("unexpected number of IPs for network %s", d.Network)
	}
	if nw.IPs[0].Address == "" {
		return fmt.Errorf("%s network doesn't have DHCP configured", d.Network)
	}
	// Corner case, but might happen...
	if active, err := network.IsActive(); !active {
		log.Debugf("Reactivating network: %s", err)
		err = network.Create()
		if err != nil {
			log.Warnf("Failed to Start network: %s", err)
			return err
		}
	}
	return nil
}

func (d *Driver) PreCreateCheck() error {
	conn, err := d.getConn()
	if err != nil {
		return err
	}

	// TODO We could look at conn.GetCapabilities()
	// parse the XML, and look for kvm
	log.Debug("About to check libvirt version")

	// TODO might want to check minimum version
	_, err = conn.GetLibVersion()
	if err != nil {
		log.Warnf("Unable to get libvirt version")
		return err
	}
	err = d.validateNetwork()
	if err != nil {
		return err
	}

	err = d.validateStoragePool()
	if err != nil {
		return err
	}
	// Others...?
	return nil
}

func (d *Driver) getDiskImageFilename() string {
	return fmt.Sprintf("%s.%s", d.MachineName, d.ImageFormat)
}

func (d *Driver) getDiskImagePath() string {
	return d.ResolveStorePath(fmt.Sprintf("%s.%s", d.MachineName, d.ImageFormat))
}

func (d *Driver) setupDiskImage() error {
	diskPath := d.getDiskImagePath()

	log.Debugf("Preparing %s for machine use", diskPath)
	if d.ImageFormat != "qcow2" {
		return fmt.Errorf("Unsupported VM image format: %s", d.ImageFormat)
	}

	if err := createImage(d.ImageSourcePath, diskPath); err != nil {
		return err
	}

	/* If createImage uses libvirt APIs to create the overlay qcow2 file,
	 * an explicit pool refresh won't be needed
	 */
	if err := d.refreshStoragePool(); err != nil {
		return err
	}

	// Libvirt typically runs as a deprivileged service account and
	// needs the execute bit set for directories that contain disks
	for dir := d.ResolveStorePath("."); dir != "/"; dir = filepath.Dir(dir) {
		log.Debugf("Verifying executable bit set on %s", dir)
		info, err := os.Stat(dir)
		if err != nil {
			return err
		}
		mode := info.Mode()
		if mode&0001 != 1 {
			log.Debugf("Setting executable bit set on %s", dir)
			mode |= 0001
			if err := os.Chmod(dir, mode); err != nil {
				return err
			}
		}
	}

	return nil
}

func (d *Driver) Create() error {
	err := d.setupDiskImage()
	if err != nil {
		return err
	}

	log.Debugf("Defining VM...")
	xml, err := domainXML(d)
	if err != nil {
		return err
	}

	conn, err := d.getConn()
	if err != nil {
		return err
	}

	vm, err := conn.DomainDefineXML(xml)
	if err != nil {
		log.Warnf("Failed to create the VM: %s", err)
		return err
	}
	d.vm = vm
	d.vmLoaded = true
	log.Debugf("Adding the file: %s", filepath.Join(d.ResolveStorePath("."), fmt.Sprintf(".%s-exist", d.MachineName)))
	_, _ = os.OpenFile(filepath.Join(d.ResolveStorePath("."), fmt.Sprintf(".%s-exist", d.MachineName)), os.O_RDONLY|os.O_CREATE, 0666)

	return d.Start()
}

func createImage(src, dst string) error {
	start := time.Now()
	defer func() {
		log.Debugf("image creation took %s", time.Since(start).String())
	}()
	// #nosec G204
	cmd := exec.Command("qemu-img",
		"create",
		"-f", "qcow2",
		"-F", "qcow2",
		"-o", fmt.Sprintf("backing_file=%s", src),
		dst)
	if err := cmd.Run(); err != nil {
		log.Debugf("qemu-img create failed, falling back to copy: %v", err)
		return mcnutils.CopyFile(src, dst)
	}
	return nil
}

func (d *Driver) Start() error {
	log.Debugf("Starting VM %s", d.MachineName)
	if err := d.validateVMRef(); err != nil {
		return err
	}
	if err := d.validateNetwork(); err != nil {
		return err
	}
	if err := d.validateStoragePool(); err != nil {
		return err
	}

	if d.DiskCapacity == 0 {
		diskCapacity, err := d.getVolCapacity()
		if err != nil {
			return err
		}
		d.DiskCapacity = diskCapacity
	}

	if err := d.vm.Create(); err != nil {
		log.Warnf("Failed to start: %s", err)
		return err
	}

	if d.Network == "" {
		return nil
	}

	// They wont start immediately
	time.Sleep(5 * time.Second)

	for i := 0; i < 60; i++ {
		ip, err := d.GetIP()
		if err != nil {
			return fmt.Errorf("%v: getting ip during machine start", err)
		}

		if ip == "" {
			log.Debugf("Waiting for machine to come up %d/%d", i, 60)
			time.Sleep(3 * time.Second)
			continue
		}

		if ip != "" {
			log.Infof("Found IP for machine: %s", ip)
			d.IPAddress = ip
			break
		}
		log.Debugf("Waiting for the VM to come up... %d", i)
	}

	if d.IPAddress == "" {
		log.Warnf("Unable to determine VM's IP address, did it fail to boot?")
	}
	return nil
}

func (d *Driver) Stop() error {
	log.Debugf("Stopping VM %s", d.MachineName)
	if err := d.validateVMRef(); err != nil {
		return err
	}
	s, err := d.GetState()
	if err != nil {
		return err
	}

	if s != state.Stopped {
		err := d.vm.Shutdown()
		if err != nil {
			log.Warnf("Failed to gracefully shutdown VM")
			return err
		}
		for i := 0; i < 120; i++ {
			time.Sleep(time.Second)
			s, _ := d.GetState()
			log.Debugf("VM state: %s", s)
			if s == state.Stopped {
				return nil
			}
		}
		return errors.New("VM Failed to gracefully shutdown, try the kill command")
	}
	return nil
}

func (d *Driver) Remove() error {
	log.Debugf("Removing VM %s", d.MachineName)
	if err := d.validateVMRef(); err != nil {
		return err
	}
	// Note: If we switch to qcow disks instead of raw the user
	//       could take a snapshot.  If you do, then Undefine
	//       will fail unless we nuke the snapshots first
	_ = d.vm.Destroy() // Ignore errors
	return d.vm.Undefine()
}

func (d *Driver) Restart() error {
	log.Debugf("Restarting VM %s", d.MachineName)
	if err := d.Stop(); err != nil {
		return err
	}
	return d.Start()
}

func (d *Driver) Kill() error {
	log.Debugf("Killing VM %s", d.MachineName)
	if err := d.validateVMRef(); err != nil {
		return err
	}
	return d.vm.Destroy()
}

func (d *Driver) GetState() (state.State, error) {
	log.Debugf("Getting current state...")
	if err := d.validateVMRef(); err != nil {
		return state.None, err
	}
	virState, _, err := d.vm.GetState()
	if err != nil {
		return state.None, err
	}
	switch virState {
	case libvirt.DOMAIN_NOSTATE:
		return state.None, nil
	case libvirt.DOMAIN_RUNNING:
		return state.Running, nil
	case libvirt.DOMAIN_BLOCKED:
		// TODO - Not really correct, but does it matter?
		return state.Error, nil
	case libvirt.DOMAIN_PAUSED:
		return state.Paused, nil
	case libvirt.DOMAIN_SHUTDOWN:
		return state.Stopped, nil
	case libvirt.DOMAIN_CRASHED:
		return state.Error, nil
	case libvirt.DOMAIN_PMSUSPENDED:
		return state.Saved, nil
	case libvirt.DOMAIN_SHUTOFF:
		return state.Stopped, nil
	}
	return state.None, nil
}

func (d *Driver) validateVMRef() error {
	if !d.vmLoaded {
		log.Debugf("Fetching VM...")
		conn, err := d.getConn()
		if err != nil {
			return err
		}
		vm, err := conn.LookupDomainByName(d.MachineName)
		if err != nil {
			log.Warnf("Failed to fetch machine")
			return fmt.Errorf("Failed to fetch machine '%s'", d.MachineName)
		}
		d.vm = vm
		d.vmLoaded = true
	}
	return nil
}

func (d *Driver) GetIP() (string, error) {
	log.Debugf("GetIP called for %s", d.MachineName)
	s, err := d.GetState()
	if err != nil {
		return "", fmt.Errorf("%v : machine in unknown state", err)
	}
	if s != state.Running {
		return "", errors.New("host is not running")
	}
	ifaces, err := d.vm.ListAllInterfaceAddresses(libvirt.DOMAIN_INTERFACE_ADDRESSES_SRC_LEASE)
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Hwaddr == macAddress {
			for _, addr := range iface.Addrs {
				if addr.Type == int(libvirt.IP_ADDR_TYPE_IPV4) { // ipv4
					log.Debugf("IP address: %s", addr.Addr)
					return addr.Addr, nil
				}
			}
		}
	}
	return "", nil
}

func NewDriver(hostName, storePath string) drivers.Driver {
	return &Driver{
		Driver: &libvirtdriver.Driver{
			VMDriver: &drivers.VMDriver{
				BaseDriver: &drivers.BaseDriver{
					MachineName: hostName,
					StorePath:   storePath,
					SSHUser:     DefaultSSHUser,
				},
			},
			Network:     DefaultNetwork,
			StoragePool: DefaultPool,
		},
	}
}
