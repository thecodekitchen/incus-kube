package main

import (
	"os"

	remote "github.com/pulumi/pulumi-command/sdk/go/command/remote"
	"github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func SetupKube(ctx *pulumi.Context, vm_stack IncusNodes) (*kubernetes.Provider, error) {
	privKey, err := os.ReadFile("./pulumi_key")
	if err != nil {
		return &kubernetes.Provider{}, err
	}

	// Define the SSH connection for the master
	masterConn := &remote.ConnectionArgs{
		Host:           vm_stack.MasterArchNode.Ipv4Address,
		User:           pulumi.String("pulumi"),
		PrivateKey:     pulumi.String(privKey),
		DialErrorLimit: pulumi.Int(10),
	}
	// This dummy command will retry until cloud-init has started sshd.
	// The built-in retry logic of the provider will handle the "connection refused".
	masterSshReady, err := remote.NewCommand(ctx, "wait-for-master-ssh", &remote.CommandArgs{
		Connection: masterConn,
		Create:     pulumi.String("echo 'SSH is ready'"),
	}, pulumi.DependsOn([]pulumi.Resource{vm_stack.MasterArchNode})) // Depends on the VM
	if err != nil {
		return &kubernetes.Provider{}, err
	}

	// === NEW: Reboot the node to load the new kernel ===
	// This depends on the first SSH wait.
	// We run the reboot in the background (&) so the command exits 0
	// and Pulumi doesn't see a "failed" SSH connection.
	rebootCmd, err := remote.NewCommand(ctx, "reboot-master", &remote.CommandArgs{
		Connection: masterConn,
		Create:     pulumi.String("sudo reboot &"),
	}, pulumi.DependsOn([]pulumi.Resource{masterSshReady}))
	if err != nil {
		return &kubernetes.Provider{}, err
	}

	// === NEW: Wait for SSH *after* the reboot ===
	// This depends on the reboot command. It will start polling.
	// The provider's `DialErrorLimit` will handle the "connection refused"
	// retries until the VM is back up.
	masterSshReadyAfterReboot, err := remote.NewCommand(ctx, "master-wait-ssh-reboot", &remote.CommandArgs{
		Connection: masterConn,
		Create:     pulumi.String("echo 'SSH is ready after reboot'"),
	}, pulumi.DependsOn([]pulumi.Resource{rebootCmd}))
	if err != nil {
		return &kubernetes.Provider{}, err
	}
	// This command installs K8s prerequisites and runs 'kubeadm init'
	// We add a 'dependsOn' to ensure it only runs after the VM is ready.
	kubeInitCmd, err := remote.NewCommand(ctx, "kube-init", &remote.CommandArgs{
		Connection: masterConn,
		Create: pulumi.String(`
			set -e
			echo "Enabling overlay module"
			# Load the 'overlay' module for the current boot
			sudo modprobe overlay
			# Ensure the 'overlay' module loads on all future boots
			echo overlay | sudo tee /etc/modules-load.d/k3s.conf
			echo "Installing K3s on master..."
			# This single command downloads K3s, installs it, enables it,
			# and configures it as a server.
			# It also automatically creates the kubeconfig at /etc/rancher/k3s/k3s.yaml
			curl -sfL https://get.k3s.io | sh -
		`),
	}, pulumi.DependsOn([]pulumi.Resource{vm_stack.MasterArchNode, masterSshReadyAfterReboot}))
	if err != nil {
		return &kubernetes.Provider{}, err
	}
	k3sUrl := pulumi.Sprintf("https://%s:6443", vm_stack.MasterArchNode.Ipv4Address)
	// === NEW: Get Kubeconfig from Master ===
	// This command cats the admin config and its output is used by the K8s provider
	getKubeconfigCmd, err := remote.NewCommand(ctx, "get-kubeconfig", &remote.CommandArgs{
		Connection: masterConn,
		// The path is /etc/rancher/k3s/k3s.yaml
		// We use Sprintf to inject the master's IP, replacing the
		// default '127.0.0.1' that K3s puts in its config.
		Create: pulumi.Sprintf(
			"sudo cat /etc/rancher/k3s/k3s.yaml | sed 's/127.0.0.1/%s/'",
			vm_stack.MasterArchNode.Ipv4Address,
		),
	}, pulumi.DependsOn([]pulumi.Resource{kubeInitCmd}))
	if err != nil {
		return &kubernetes.Provider{}, err
	}

	// Get the join command from the master
	// This command depends on the 'kubeInitCmd' completing successfully.
	// --- 6. Get Join Token ---
	getJoinTokenCmd, err := remote.NewCommand(ctx, "get-join-token", &remote.CommandArgs{
		Connection: masterConn,
		Create:     pulumi.Sprintf("sudo cat /var/lib/rancher/k3s/server/node-token\n"),
	}, pulumi.DependsOn([]pulumi.Resource{kubeInitCmd}))
	if err != nil {
		return &kubernetes.Provider{}, err
	}

	// --- Join Worker 1 (Ubuntu) ---
	worker1Conn := &remote.ConnectionArgs{
		Host:       vm_stack.WorkerUbuntuNode.Ipv4Address,
		User:       pulumi.String("pulumi"),
		PrivateKey: pulumi.String(privKey),
	}

	worker1SshReady, err := remote.NewCommand(ctx, "wait-for-worker1-ssh", &remote.CommandArgs{
		Connection: worker1Conn,
		Create:     pulumi.String("echo 'SSH is ready'"),
	}, pulumi.DependsOn([]pulumi.Resource{vm_stack.WorkerUbuntuNode, getJoinTokenCmd})) // Depends on the VM
	if err != nil {
		return &kubernetes.Provider{}, err
	}

	worker1JoinCmd, err := remote.NewCommand(ctx, "worker1-join", &remote.CommandArgs{
		Connection: worker1Conn,
		Create: pulumi.Sprintf(`
			set -e
			echo "Installing K3s on worker (Ubuntu)..."
			# We pass the master URL and Token as environment variables
			curl -sfL https://get.k3s.io | K3S_URL=%s K3S_TOKEN=%s sh -
		`, k3sUrl, getJoinTokenCmd.Stdout), // <-- Uses the token here
	}, pulumi.DependsOn([]pulumi.Resource{worker1SshReady, getJoinTokenCmd}))
	if err != nil {
		return &kubernetes.Provider{}, err
	}

	// --- Join Worker 2 (Alma) ---
	worker2Conn := &remote.ConnectionArgs{
		Host:       vm_stack.WorkerAlmaNode.Ipv4Address,
		User:       pulumi.String("pulumi"),
		PrivateKey: pulumi.String(privKey),
	}
	worker2SshReady, err := remote.NewCommand(ctx, "wait-for-worker2-ssh", &remote.CommandArgs{
		Connection: worker2Conn,
		Create:     pulumi.String("echo 'SSH is ready'"),
	}, pulumi.DependsOn([]pulumi.Resource{vm_stack.WorkerAlmaNode, getJoinTokenCmd})) // Depends on the VM
	if err != nil {
		return &kubernetes.Provider{}, err
	}

	worker2JoinCmd, err := remote.NewCommand(ctx, "worker2-join", &remote.CommandArgs{
		Connection: worker1Conn,
		Create: pulumi.Sprintf(`
			set -e
			echo "Installing K3s on worker (Ubuntu)..."
			# We pass the master URL and Token as environment variables
			curl -sfL https://get.k3s.io | K3S_URL=%s K3S_TOKEN=%s sh -
		`, k3sUrl, getJoinTokenCmd.Stdout), // <-- Uses the token here
	}, pulumi.DependsOn([]pulumi.Resource{worker2SshReady, getJoinTokenCmd}))
	if err != nil {
		return &kubernetes.Provider{}, err
	}

	// === NEW: Instantiate the Kubernetes Provider ===
	// We pass the stdout of the getKubeconfigCmd *directly* to the provider.
	return kubernetes.NewProvider(ctx, "k8s-provider", &kubernetes.ProviderArgs{
		Kubeconfig: getKubeconfigCmd.Stdout,
	}, pulumi.DependsOn([]pulumi.Resource{kubeInitCmd, worker1JoinCmd, worker2JoinCmd})) // Depends on CN
}
