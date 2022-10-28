package main

import (
	"fmt"
	"os"

	"github.com/code-ready/machine-driver-libvirt/pkg/libvirt"
	"github.com/crc-org/machine/libmachine/drivers/plugin"
)

func main() {
	if len(os.Args) > 1 {
		if os.Args[1] == "version" {
			fmt.Printf("Driver version: %s\n", libvirt.DriverVersion)
			os.Exit(0)
		}
	}
	plugin.RegisterDriver(libvirt.NewDriver("default", "path"))
}
