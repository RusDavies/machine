// Notes:
// 1. The following is an attempt to add provisioning code to support Fedora images.
//    It's mostly a copy of the ubuntu.go code.  As such, it's not very DRY, and
//    improvements could be acheived with a more considered approach. 
//
// 2. Since Fedora 15, systemd has replaced sysvinit by default.  At the time of 
//    writing, the current version of Fedora is 21, and there's still some proxying
//    behaviour between the 'service' and 'systemctl' commands.  However, we 
//    can't always rely on this being the case, so it's better to set things 
//    straight from the get go.  We should use systemd if available.  The check
//    is performed by the FedoraProvisioner.SystemdCheck() method, with the state
//	  being stored as a bool in FedoraProvisioner.SystemdEnabled
//
//    It's worth noting that recent versions of other major distributions are 
//    also using systemd by default [*]. Therefore, it may be relevant 
//    to instead attach the SystemdCheck() method and SystemdEnabled field to 
//    the GenericProvisioner.
//    [*] http://en.wikipedia.org/wiki/Systemd#Adoption_and_reception
//
// 3. By default, Fedora does not enable IP packet forwarding, which is needed
//    for containers to be routable to/from the outside world. To enable forwarding,
//    one must execute:
//       sudo sysctl -w net.ipv4.ip_forward=1
//
//    To make this change persistent between reboots, one should execute:
//       sudo sh -c 'echo net.ipv4.ip_forward = 1 >> /etc/sysctl.d/80-docker.conf'
//
//    The purpose of the new FedoraProvisioner.EnableIpForwarding() method is to
//    perform both commands.
//
// 
// 4. Fedora has the docker-io package available.  Therefore, one may make use of 
//    that, by including docker in the package list.  One can then skip the 
//    installDockerGeneric() step in FedoraProvisioner.Provision(...)
//
//
// 5. In Fedora, the systemd service script for docker is located at /usr/lib/systemd/system
//    It loads the following three files:
//        /etc/sysconfig/docker
//        /etc/sysconfig/docker-storage
//        /etc/sysconfig/docker-network
//
//    From those three files, the service script makes use of four environment
//    variables: 
//        OPTIONS
//        DOCKER_STORAGE_OPTIONS
//        DOCKER_NETWORK_OPTIONS 
//        INSECURE_REGISTRY
//
//    The docker service is launched as:
//       /usr/bin/docker -d $OPTIONS \
//                          $DOCKER_STORAGE_OPTIONS \
//                          $DOCKER_NETWORK_OPTIONS \
//  						$INSECURE_REGISTRY
//    
//    Given the way the environment variables are used, then it's possible 
//    to dump all options into just one of the environment variables (e.g. OPTIONS).
//    (However, this is not consistent with the seperation of concerns that Fedora 
//    appears to be attempting - kind of a shame to barf all over someone's nice
//    clean work ;o)  )
//
//    None of the environment variables used by the fedora service script match the 
//    the DOCKER_OPTS that, to date, has been written out by 
//    GenericProvisioner.GenerateDockerOptions() in generic.go.  To provide greater 
//    flexibility, that environment variable name has been parameterized in the 
//    template, configurable via GenericProvisioner.DockerOptsEnvVar.  The 
//    UbuntuProvisioner in ubuntu.go has been updated to include the origin 
//    value of "DOCKER_OPTS", which should allow it to work as per normal.
//  
package provision

import (
	"fmt"
	"bytes"

	"github.com/docker/machine/drivers"
	"github.com/docker/machine/libmachine/auth"
	"github.com/docker/machine/libmachine/engine"
	"github.com/docker/machine/libmachine/provision/pkgaction"
	"github.com/docker/machine/libmachine/swarm"
	"github.com/docker/machine/log"
	"github.com/docker/machine/utils"
)

type FedoraProvisionerExt struct {
    SystemdEnabled 		bool
}

type FedoraProvisioner struct {
	GenericProvisioner
	FedoraProvisionerExt
}

func NewFedoraProvisioner(d drivers.Driver) Provisioner {
	return &FedoraProvisioner{
		GenericProvisioner{
			DockerOptionsDir:  "/etc/docker",
			DaemonOptionsFile: "/etc/sysconfig/docker",
			DockerOptsEnvVar:  "OPTIONS",
			OsReleaseId:       "fedora",
			Packages: []string{
				"curl",       
				"docker",
			},
			Driver: d,
		},
   		FedoraProvisionerExt{
			SystemdEnabled: true,
			},
	}
}

func init() {
	Register("Fedora", &RegisteredProvisioner{
		New: NewFedoraProvisioner,
	})
}

func (provisioner *FedoraProvisioner) SystemdCheck() error {
	// See note 1 at head of document.
	var (
		command 	string
		reader		bytes.Buffer
	)

	// Command to test for sysvint or systemd on Fedora
	command = "pidof /sbin/init &>/dev/null && echo sysvinit || echo systemd" 
	response, err := provisioner.SSHCommand(command)
	if err != nil {
		return err
	}
	
	if _, err := reader.ReadFrom(response.Stdout); err != nil {
		return err
	}
	
	result := reader.String()
	if result == "systemd" {
		provisioner.SystemdEnabled = true
	} else {
		provisioner.SystemdEnabled = false
	}
	
	return nil
}

func (provisioner *FedoraProvisioner) EnableIpForwarding() error {
	var command string
	
	// Command to enable IP forwarding 
	command = "sudo sysctl -w net.ipv4.ip_forward=1"
	if _, err := provisioner.SSHCommand(command); err != nil {
		log.Debug("Failed to run SSH command to enable ip forwarding to docker containers")
		return err
	}
	
	// Command to persist enablement of IP forwarding between boots
	command = "sudo sh -c 'echo net.ipv4.ip_forward = 1 >> /etc/sysctl.d/80-docker.conf'"
	if _, err := provisioner.SSHCommand(command); err != nil {
		log.Debug("Failed to run SSH command to make ip forwarding to docker containers permanent")
		return err
	}
	
	return nil
}

func (provisioner *FedoraProvisioner) Service(name string, action pkgaction.ServiceAction) error {
	var command string; 
	log.Debugf("DEBUG: FedoraProvisioner.Service() - called")

	// The command varies depending on whether the host is using sysvinit or systemd
	if provisioner.SystemdEnabled {
		command = fmt.Sprintf("sudo systemctl %s %s", action.String(), name)  // systemd method
	} else {
		command = fmt.Sprintf("sudo service %s %s", name, action.String()) // SysVinit method
	}	

	log.Debugf(fmt.Sprintf("DEBUG: FedoraProvisioner.Service() - about to run: %s", command) )
	if _, err := provisioner.SSHCommand(command); err != nil {
		return err
	}
	log.Debugf("DEBUG: Service() - OK")

	return nil
}

func (provisioner *FedoraProvisioner) Package(name string, action pkgaction.PackageAction) error {
	var packageAction string

	switch action {
	case pkgaction.Install:
		packageAction = "install"
	case pkgaction.Remove:
		packageAction = "remove"
	case pkgaction.Upgrade:
		packageAction = "update" // TODO: Check that apt-get upgrade => yum update
	}

	// TODO: This should probably have a const
	switch name {
	case "docker":
		name = "docker-io" 
	}

	command := fmt.Sprintf("sudo yum -y %s %s", packageAction, name)

	if _, err := provisioner.SSHCommand(command); err != nil {
		return err
	}

	return nil
}

func (provisioner *FedoraProvisioner) dockerDaemonResponding() bool {
	if _, err := provisioner.SSHCommand("sudo docker version"); err != nil {
		log.Warnf("Error getting SSH command to check if the daemon is up: %s", err)
		return false
	}

	// The daemon is up if the command worked.  Carry on.
	return true
}

func (provisioner *FedoraProvisioner) Provision(swarmOptions swarm.SwarmOptions, authOptions auth.AuthOptions, engineOptions engine.EngineOptions) error {
	provisioner.SwarmOptions = swarmOptions
	provisioner.AuthOptions = authOptions
	provisioner.EngineOptions = engineOptions

	if provisioner.EngineOptions.StorageDriver == "" {
		provisioner.EngineOptions.StorageDriver = "aufs"
	}

	if err := provisioner.SetHostname(provisioner.Driver.GetMachineName()); err != nil {
		return err
	}

	if err := provisioner.EnableIpForwarding(); err != nil {
		return err
	}

	if err := provisioner.SystemdCheck(); err != nil {
		return err
	}

	for _, pkg := range provisioner.Packages {
		if err := provisioner.Package(pkg, pkgaction.Install); err != nil {
			return err
		}
	}

	// Not necassary (TBC) since Fedora gets docker from package list.  
	//if err := installDockerGeneric(provisioner); err != nil {
	//	return err
	//}
	
	// Docker has to be started for the subsequent test to work
	if err := provisioner.Service("docker", pkgaction.Restart); err != nil {
		return err
	}
		
	if err := utils.WaitFor(provisioner.dockerDaemonResponding); err != nil {
		return err
	}

	if err := makeDockerOptionsDir(provisioner); err != nil {
		return err
	}

	provisioner.AuthOptions = setRemoteAuthOptions(provisioner)

	if err := ConfigureAuth(provisioner); err != nil {
		return err
	}

	if err := configureSwarm(provisioner, swarmOptions); err != nil {
		return err
	}

	return nil
}
