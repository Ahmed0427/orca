package container

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

const (
	BridgeName    = "mini0"
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

func CreateContainer(id string) {
	EnsureBridge()

	name := id[len(id)-8:]

	RunCmd("ip", "netns", "add", name)

	hostVeth := fmt.Sprintf("veth-%s", name)
	RunCmd("ip", "link", "add", hostVeth, "type", "veth", "peer", "name", "eth0")

	RunCmd("ip", "link", "set", "eth0", "netns", name)

	RunCmd("ip", "link", "set", hostVeth, "master", BridgeName)
	RunCmd("ip", "link", "set", hostVeth, "up")

	ip := AllocateIP()

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
	if err := exec.Command("ip", "link", "show", BridgeName).Run(); err == nil {
		return
	}

	RunCmd("ip", "link", "add", "name", BridgeName, "type", "bridge")
	RunCmd("ip", "addr", "add", BridgeIP, "dev", BridgeName)
	RunCmd("ip", "link", "set", BridgeName, "up")

	if err := os.WriteFile(IPForwardFile, []byte("1"), 0644); err != nil {
		log.Printf("Warning: could not enable ip_forward: %v", err)
	}

	SetupForwardRules()
	SetupMasquerade()
}

func SetupForwardRules() {
	b := BridgeName
	// forward traffic within the bridge (container <-> container)
	AddRuleIfMissing("filter", "FORWARD", "-i", b, "-o", b, "-j", "ACCEPT")
	// forward traffic from bridge to outside
	AddRuleIfMissing("filter", "FORWARD", "-i", b, "!", "-o", b, "-j", "ACCEPT")
	// allow return traffic
	AddRuleIfMissing("filter", "FORWARD", "-o", b, "-j", "ACCEPT")
}

func SetupMasquerade() {
	AddRuleIfMissing("nat", "POSTROUTING",
		"-s", SubnetCIDR, "!", "-o", BridgeName, "-j", "MASQUERADE")
}

func AddRuleIfMissing(table, chain string, rule ...string) {
	args := append([]string{"-t", table, "-C", chain}, rule...)
	if err := RunCmdSilent("iptables", args...); err == nil {
		return // rule exists
	}
	args = append([]string{"-t", table, "-A", chain}, rule...)
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

func CleanupContainer(id string) {
	name := id[len(id)-8:]
	vethHost := fmt.Sprintf("veth-%s", name)
	_ = RunCmdSilent("ip", "link", "del", vethHost)
	_ = RunCmdSilent("ip", "netns", "del", name)
}

func CleanupAll() {
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

	_ = RunCmdSilent("ip", "link", "del", b)

	_ = RunCmdSilent("iptables", "-t", "nat", "-D", "POSTROUTING",
		"-s", SubnetCIDR, "!", "-o", b, "-j", "MASQUERADE")

	_ = RunCmdSilent("iptables", "-D", "FORWARD", "-i", b, "-o", b, "-j", "ACCEPT")

	_ = RunCmdSilent("iptables", "-D", "FORWARD", "-i", b, "!", "-o", b, "-j", "ACCEPT")

	_ = RunCmdSilent("iptables", "-D", "FORWARD", "-o", b, "-j", "ACCEPT")
}
