# Incus Kube
### A Kubernetes cluster joined across three different Linux distros on Incus VMs

This repo demonstrates how to use Incus through the Pulumi provider in Go to deploy 3 separate VM nodes and join them into a Kubernetes cluster along with structured workload deployments on an Ubuntu host. 

It attempts to showcase the differences between how three different package management strategies and security profiles in Linux operating systems can be accommodated within the same cluster configuration.

# Contents

- Master Node - Arch Cloud:
    - Cloud init works with pacman to set up ssh interface
    - Uses k3s for its Kubernetes runtime
- Worker Node 1: Ubuntu 25.04 Cloud:
    - Cloud init works with apt to get curl and set up ssh
    - Joins with generated join command
- Worker Node 2: Alma 9 Cloud:
    - Needs a separate cloud init drive to enable setup
    - Cloud init creates user, unlocks it for public key auth only, sets up ssh
    - Joins with generated join command

# Host Setup

This assumes an Ubuntu host running ufw. Users of other host distros or firewalls will need to translate into their context.

**WARNING**: This is standard system hygiene, but you should MAKE SURE ufw is enabled with 
```
sudo ufw enable
```
before following this setup process.

1. Install Incus
    ```
    sudo apt install incus
    ```
2. Install Pulumi
    ```
    curl -fsSL https://get.pulumi.com | sh
    ```
3. Add or modify the following line in /etc/default/ufw
    ```
    DEFAULT_FORWARD_POLICY="ACCEPT"
    ```
4. Add the following lines to the top of /etc/ufw/before.rules JUST BEFORE the required *filter section marked "Do not edit". Replace PRIMARY_NETWORK_INTERFACE with the network interface name you want to use for the nodes to access the internet for package downloads.
    ```
    #
    # NAT table rules
    #
    *nat
    :POSTROUTING ACCEPT [0:0]

    # Allow traffic from Incus VMs (10.10.10.0/24) to the internet
    -A POSTROUTING -s 10.10.10.0/24 -o <PRIMARY_NETWORK_INTERFACE> -j MASQUERADE

    COMMIT
    #
    # End of NAT table rules
    #
    ```

# Installing and Running

1. Clone the repo
    ```
    git clone https://github.com/thecodekitchen/incus-kube
    cd incus-kube
    ```
2. Generate an appropriate private/public key pair to securely control the VMs with the provided script.
    ```
    . keygen.sh
    ```
3. Stage 1 (for now): Run 'pulumi up -y' to auto-accept the plan and create the VM stack or drop the -y to review it first. This initial run will 'fail' because the DHCP appears to be resetting the master node's IP during the cloud init process, necessitating a refresh between stages. This issue is under investigation for viable workarounds.
    ```
    pulumi up -y
    ```
4. After the initial run fails, run 
    ```
    sudo incus shell kube-master
    ```
    to access the running Arch master node.
    Then, run
    ```
    tail -f /var/log/cloud-init-output.log
    ```
    to monitor the cloud-init's progress until it reads something like 
    ```
    Cloud-init v. 25.3 finished at Sat, 15 Nov 2025 21:19:16 +0000. Datasource DataSourceNoCloud [seed=/var/lib/cloud/seed/nocloud-net].  Up 111.26 seconds
    ```
    Once it does, proceed to the next step.

5. Run pulumi refresh to update the Pulumi state with the newly assigned node IPs after cloud init.
    ```
    pulumi refresh -y
    ```
6. Stage 2 (for now): Run pulumi up a second time. This time, the cloud init should have completed and all the VMs should have what they need to be connected to with SSH for the rest of the Kubernetes setup commands to be run from the host machine using the generated key pair.
    ```
    pulumi up -y
    ```
7. When everything completes, access the master node through Incus again and confirm that kubectl shows the nginx pod from the deployment file. If it's there, you've got a kube!
    ```
    sudo incus shell kube-master

    pulumi@kube-master:~ kubectl get pods
    ```
