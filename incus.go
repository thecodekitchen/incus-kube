package main

import (
	"os"

	"github.com/pulumi/pulumi-command/sdk/go/command/local"
	"github.com/pulumi/pulumi-terraform-provider/sdks/go/incus/incus"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Template for Arch Linux (Master)
// This includes the critical pacman-key init steps
const archCloudInitTemplate = `#cloud-config
users:
  - name: pulumi
    sudo: ALL=(ALL) NOPASSWD:ALL
    groups: [sudo, admin]
    shell: /bin/bash
    ssh_authorized_keys:
      - %s
runcmd:
  - 'pacman-key --init'
  - 'pacman-key --populate'
  - 'pacman -Syu --noconfirm'
  - 'pacman -S --noconfirm openssh'
  - 'systemctl enable --now sshd'
`

// Template for Ubuntu (Worker 1)
const ubuntuCloudInitTemplate = `#cloud-config
users:
  - name: pulumi
    sudo: ALL=(ALL) NOPASSWD:ALL
    groups: [sudo, admin]
    shell: /bin/bash
    ssh_authorized_keys:
      - %s
runcmd:
  - 'apt-get update'
  - 'apt-get install -y openssh-server curl'
  - 'systemctl enable --now ssh'
`

// Template for AlmaLinux (Worker 2)
const almaCloudInitTemplate = `#cloud-config
users:
  - default
  - name: pulumi
    # On AlmaLinux 9, the administrative group is 'wheel', not 'sudo'
    groups: [ wheel ]
    # 1. Explicitly set 'lock_passwd: false'.
    #    The 'cloud-init' default can sometimes be 'true'.[1]
    lock_passwd: false
    # 2. Set the password hash to '*', an invalid, non-empty
    #    value. This disables password login without "locking"
    #    the account for key-based authentication.
    passwd: '*'
    # Set the user's shell
    shell: /bin/bash
    # Provide one or more public SSH keys.
    # CRITICAL: These MUST NOT be legacy 'ssh-rsa' keys.
    # Use 'ssh-ed25519', 'ecdsa-sha2-nistp256', or 'rsa-sha2-512'.
    ssh_authorized_keys:
      - %s
# 2. Run all setup commands in the correct sequence
runcmd:
  - 'yum install -y curl openssh-server'
  - 'systemctl enable --now sshd'
`

type IncusNodes struct {
	MasterArchNode   *incus.Instance
	WorkerUbuntuNode *incus.Instance
	WorkerAlmaNode   *incus.Instance
}

func LaunchVMs(ctx *pulumi.Context) (IncusNodes, error) {
	// 1. Create a shared storage pool for the VMs
	// We'll use the 'dir' driver for simplicity.
	storagePool, err := incus.NewStoragePool(ctx, "kube-storage-pool", &incus.StoragePoolArgs{
		Name:   pulumi.String("k8s-pool"),
		Driver: pulumi.String("dir"),
	})
	if err != nil {
		return IncusNodes{}, err
	}

	// 2. Create the managed bridge network
	// This network will provide NAT'd internet access and DHCP.
	kubeNetwork, err := incus.NewNetwork(ctx, "kube-network", &incus.NetworkArgs{
		Name: pulumi.String("k8s-net"),
		Type: pulumi.String("bridge"),
		Config: pulumi.StringMap{
			"ipv4.address": pulumi.String("10.10.10.1/24"), // Assign a private subnet
			"ipv4.nat":     pulumi.String("true"),          // Enable NAT for internet
			"dns.mode":     pulumi.String("dynamic"),       // Allow instances to get DNS
		},
	})
	if err != nil {
		return IncusNodes{}, err
	}

	firewallRule, err := local.NewCommand(ctx, "ufw-forward-rule", &local.CommandArgs{
		Create: pulumi.String("sudo ufw allow in on k8s-net"),
		Delete: pulumi.String("sudo ufw delete allow in on k8s-net"),
	}, pulumi.DependsOn([]pulumi.Resource{
		kubeNetwork, // <-- !! UNCOMMENT THIS !!
		// Make this depend on your actual Incus network resource.
	}))
	if err != nil {
		return IncusNodes{}, err
	}

	pubKey, err := os.ReadFile("./pulumi_key.pub")
	if err != nil {
		return IncusNodes{}, err
	}

	// Format the cloud-init script with the public key

	archCloudConfig := pulumi.Sprintf(archCloudInitTemplate, string(pubKey))
	ubuntuCloudConfig := pulumi.Sprintf(ubuntuCloudInitTemplate, string(pubKey))
	almaCloudConfig := pulumi.Sprintf(almaCloudInitTemplate, string(pubKey))
	// 3. Define the device config for all VMs
	// This is a reusable slice that defines the root disk and network card.
	// We reference the names of the pool and network created above.
	vmDevices := incus.InstanceDeviceArray{
		// Root disk, using the storage pool
		&incus.InstanceDeviceArgs{
			Name: pulumi.String("root"),
			Type: pulumi.String("disk"),
			Properties: pulumi.StringMap{
				"path": pulumi.String("/"),
				"pool": storagePool.Name, // Dependency
			},
		},
		// Network card, using the bridge network
		&incus.InstanceDeviceArgs{
			Name: pulumi.String("eth0"),
			Type: pulumi.String("nic"),
			Properties: pulumi.StringMap{
				"network": kubeNetwork.Name, // Dependency
			},
		},
	}

	// 4. Create the Master Node (Arch Linux)
	masterNode, err := incus.NewInstance(ctx, "master-node", &incus.InstanceArgs{
		Name:      pulumi.String("kube-master"),
		Image:     pulumi.String("images:archlinux/cloud"),
		Type:      pulumi.String("virtual-machine"), // Specify VM
		Ephemeral: pulumi.Bool(false),
		Devices:   vmDevices,
		Config: pulumi.StringMap{
			"user.user-data":      archCloudConfig,
			"security.secureboot": pulumi.String("false"), // <-- THIS IS THE FIX
			"limits.cpu":          pulumi.String("2"),     // <-- ADD THIS
			"limits.memory":       pulumi.String("2GB"),   // <-- ADD THIS
		},
	}, pulumi.DependsOn([]pulumi.Resource{firewallRule}))
	if err != nil {
		return IncusNodes{}, err
	}

	// 5. Create the Worker Node (Ubuntu)
	workerNode1, err := incus.NewInstance(ctx, "worker-node-1", &incus.InstanceArgs{
		Name:      pulumi.String("kube-worker-1"),
		Image:     pulumi.String("images:ubuntu/25.04/cloud"), // Ubuntu LTS
		Type:      pulumi.String("virtual-machine"),
		Ephemeral: pulumi.Bool(false),
		Devices:   vmDevices,
		Config: pulumi.StringMap{
			"user.user-data": ubuntuCloudConfig,
			"limits.cpu":     pulumi.String("2"),   // <-- ADD THIS
			"limits.memory":  pulumi.String("2GB"), // <-- ADD THIS
		},
	}, pulumi.DependsOn([]pulumi.Resource{firewallRule}))
	if err != nil {
		return IncusNodes{}, err
	}

	// 6. Create the Worker Node (AlmaLinux)
	workerNode2, err := incus.NewInstance(ctx, "worker-node-2", &incus.InstanceArgs{
		Name:      pulumi.String("kube-worker-2"),
		Image:     pulumi.String("images:almalinux/9/cloud"), // AlmaLinux 9
		Type:      pulumi.String("virtual-machine"),
		Ephemeral: pulumi.Bool(false),
		Devices: incus.InstanceDeviceArray{
			// Root disk, from the storage pool
			&incus.InstanceDeviceArgs{
				Name: pulumi.String("root"),
				Type: pulumi.String("disk"),
				Properties: pulumi.StringMap{
					"path": pulumi.String("/"),
					"pool": storagePool.Name,
				},
			},
			// Network card, from the bridge network
			&incus.InstanceDeviceArgs{
				Name: pulumi.String("eth0"),
				Type: pulumi.String("nic"),
				Properties: pulumi.StringMap{
					"network": kubeNetwork.Name,
				},
			},
			// === THIS IS THE FIX ===
			// Add the agent:config disk for cloud-init
			&incus.InstanceDeviceArgs{
				Name: pulumi.String("agent"),
				Type: pulumi.String("disk"),
				Properties: pulumi.StringMap{
					"source": pulumi.String("agent:config"),
				},
			},
		},
		Config: pulumi.StringMap{
			"user.user-data": almaCloudConfig,
			"limits.cpu":     pulumi.String("2"),   // <-- ADD THIS
			"limits.memory":  pulumi.String("2GB"), // <-- ADD THIS
		},
	}, pulumi.DependsOn([]pulumi.Resource{firewallRule}))
	if err != nil {
		return IncusNodes{}, err
	}

	return IncusNodes{
		MasterArchNode:   masterNode,
		WorkerUbuntuNode: workerNode1,
		WorkerAlmaNode:   workerNode2,
	}, nil
}
