package main

import (
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		vm_stack, err := LaunchVMs(ctx)
		if err != nil {
			return err
		}
		ctx.Export("masterIp", vm_stack.MasterArchNode.Ipv4Address)
		ctx.Export("worker1Ip", vm_stack.WorkerUbuntuNode.Ipv4Address)
		ctx.Export("worker2Ip", vm_stack.WorkerAlmaNode.Ipv4Address)

		k8sProvider, err := SetupKube(ctx, vm_stack)
		if err != nil {
			return err
		}

		deployment, err := NginxDeployment(ctx, k8sProvider)

		ctx.Export("name", deployment.Metadata.Name())

		return nil
	})
}
