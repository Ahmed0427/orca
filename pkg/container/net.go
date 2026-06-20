package container

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

const (
	BridgeName    = "orca0"
	IPForwardFile = "/proc/sys/net/ipv4/ip_forward"
	BridgeIP      = "10.200.0.1/16"
	SubnetCIDR    = "10.200.0.0/16"
	StartIP       = 2
)

func RunCmd(name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("command failed: %s %v: %v", name, args, err)
	}
}

func RunCmdSilent(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

func CreateContainer(cc ContainerConfig) {
	id := cc.Name

	EnsureBridge()

	name := id[len(id)-8:]

	RunCmd("ip", "netns", "add", name)

	hostVeth := fmt.Sprintf("veth-%s", name)
	RunCmd("ip", "link", "add", hostVeth, "type", "veth", "peer", "name", "eth0")

	RunCmd("ip", "link", "set", "eth0", "netns", name)

	RunCmd("ip", "link", "set", hostVeth, "master", BridgeName)
	RunCmd("ip", "link", "set", hostVeth, "up")

	ip := AllocateIP()

	// strip CIDR mask before generating port maps
	SetupPortMapping(strings.Split(ip, "/")[0], cc.PortMap)

	RunCmd("ip", "netns", "exec", name,
		"ip", "addr", "add", ip, "dev", "eth0")

	RunCmd("ip", "netns", "exec", name,
		"ip", "link", "set", "eth0", "up")

	RunCmd("ip", "netns", "exec", name,
		"ip", "link", "set", "lo", "up")

	gateway := strings.Split(BridgeIP, "/")[0]

	RunCmd("ip", "netns", "exec", name,
		"ip", "route", "add", "default", "via", gateway)
}

func EnsureBridge() {
	if err := exec.Command("ip", "link", "show", BridgeName).Run(); err != nil {
		RunCmd("ip", "link", "add", "name", BridgeName, "type", "bridge")
		RunCmd("ip", "addr", "add", BridgeIP, "dev", BridgeName)
		RunCmd("ip", "link", "set", BridgeName, "up")
	}

	if err := os.WriteFile(IPForwardFile, []byte("1"), 0644); err != nil {
		log.Printf("Warning: could not enable ip_forward: %v", err)
	}

	_ = RunCmdSilent("sysctl", "-w", "net.ipv4.conf.all.route_localnet=1")
	_ = RunCmdSilent("sysctl", "-w", "net.ipv4.conf."+BridgeName+".route_localnet=1")

	_ = RunCmdSilent("iptables", "-t", "nat", "-N", "ORCA-DNAT")

	AddRuleIfMissing("nat", "OUTPUT",
		"-m", "addrtype",
		"--dst-type", "LOCAL",
		"-j", "ORCA-DNAT",
	)

	AddRuleIfMissing("nat", "PREROUTING",
		"-m", "addrtype",
		"--dst-type", "LOCAL",
		"-j", "ORCA-DNAT",
	)

	SetupForwardRules()
	SetupMasquerade()
}

func SetupForwardRules() {
	b := BridgeName
	AddRuleIfMissing("filter", "FORWARD", "-i", b, "-o", b, "-j", "ACCEPT")
	AddRuleIfMissing("filter", "FORWARD", "-i", b, "!", "-o", b, "-j", "ACCEPT")
	AddRuleIfMissing("filter", "FORWARD", "-o", b, "-j", "ACCEPT")
}

func SetupMasquerade() {
	// container traffic going outbound
	AddRuleIfMissing("nat", "POSTROUTING",
		"-s", SubnetCIDR, "!", "-o", BridgeName, "-j", "MASQUERADE")

	// masquerade traffic originating from host localhost to the container
	AddRuleIfMissing("nat", "POSTROUTING",
		"-o", BridgeName, "-m", "addrtype", "--src-type", "LOCAL", "-j", "MASQUERADE")
}

func SetupPortMapping(containerIP, portMap string) {
	if portMap == "" {
		return
	}

	ports := strings.Split(portMap, ":")
	if len(ports) != 2 {
		panic("port mapping must provide two ports in form 'hostPort:ContainerPort'")
	}

	hostPort, containerPort := ports[0], ports[1]
	target := containerIP + ":" + containerPort
	portChain := fmt.Sprintf("ORCA-P-%s", hostPort)

	_ = RunCmdSilent("iptables", "-t", "nat", "-N", portChain)
	_ = RunCmdSilent("iptables", "-t", "nat", "-F", portChain)

	AddRuleIfMissing("nat", "ORCA-DNAT", "-p", "tcp", "--dport", hostPort, "-j", portChain)

	RunCmd("iptables",
		"-t", "nat",
		"-I", portChain, "1",
		"-p", "tcp",
		"-j", "DNAT",
		"--to-destination", target,
	)
}

func AddRuleIfMissing(table, chain string, rule ...string) {
	args := append([]string{"-t", table, "-C", chain}, rule...)
	if err := RunCmdSilent("iptables", args...); err == nil {
		return
	}
	args = append([]string{"-t", table, "-I", chain, "1"}, rule...)
	RunCmd("iptables", args...)
}

func AllocateIP() string {
	entries, _ := os.ReadDir("/var/run/netns")
	lastOctet := StartIP + len(entries)
	if lastOctet > 254 {
		log.Fatalf("Subnet exhausted! Too many containers.")
	}
	return fmt.Sprintf("10.200.0.%d/16", lastOctet)
}

func CleanupContainer(id string, portMap string) {
	name := id[len(id)-8:]
	vethHost := fmt.Sprintf("veth-%s", name)
	_ = RunCmdSilent("ip", "link", "del", vethHost)
	_ = RunCmdSilent("ip", "netns", "del", name)

	if portMap != "" {
		ports := strings.Split(portMap, ":")
		if len(ports) != 2 {
			panic("port mapping must provide two ports in form 'hostPort:ContainerPort'")
		}
		hostPort := ports[0]

		// remove the link from the main chain first
		portChain := fmt.Sprintf("ORCA-P-%s", hostPort)
		_ = RunCmdSilent("iptables",
			"-t", "nat",
			"-D", "ORCA-DNAT",
			"-p", "tcp",
			"--dport", hostPort,
			"-j", portChain,
		)
		// flush and delete the custom port chain
		_ = RunCmdSilent("iptables", "-t", "nat", "-F", portChain)
		_ = RunCmdSilent("iptables", "-t", "nat", "-X", portChain)
	}
}

func CleanupNet() {
	entries, _ := os.ReadDir("/var/run/netns")
	for _, e := range entries {
		ns := e.Name()
		if ns == "default" {
			continue
		}
		vethHost := fmt.Sprintf("veth-%s", ns[:min(8, len(ns))])
		_ = RunCmdSilent("ip", "link", "del", vethHost)
		_ = RunCmdSilent("ip", "netns", "del", ns)
	}

	b := BridgeName

	_ = RunCmdSilent("iptables",
		"-t", "nat",
		"-D", "PREROUTING",
		"-m", "addrtype",
		"--dst-type", "LOCAL",
		"-j", "ORCA-DNAT",
	)

	_ = RunCmdSilent("iptables",
		"-t", "nat",
		"-D", "OUTPUT",
		"-m", "addrtype",
		"--dst-type", "LOCAL",
		"-j", "ORCA-DNAT",
	)
	_ = RunCmdSilent("iptables", "-t", "nat", "-F", "ORCA-DNAT")
	_ = RunCmdSilent("iptables", "-t", "nat", "-X", "ORCA-DNAT")

	_ = RunCmdSilent("iptables",
		"-t", "nat",
		"-D", "POSTROUTING", "-s", SubnetCIDR,
		"!", "-o", b,
		"-j", "MASQUERADE",
	)

	_ = RunCmdSilent("iptables",
		"-t", "nat",
		"-D", "POSTROUTING",
		"-o", b,
		"-m", "addrtype",
		"--src-type", "LOCAL",
		"-j", "MASQUERADE",
	)

	_ = RunCmdSilent("iptables", "-D", "FORWARD", "-i", b, "-o", b, "-j", "ACCEPT")
	_ = RunCmdSilent("iptables", "-D", "FORWARD", "-i", b, "!", "-o", b, "-j", "ACCEPT")
	_ = RunCmdSilent("iptables", "-D", "FORWARD", "-o", b, "-j", "ACCEPT")

	_ = RunCmdSilent("ip", "link", "del", b)
}
