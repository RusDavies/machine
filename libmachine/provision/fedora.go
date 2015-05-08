// Notes:
// 1. The following is an attempt to add provisioning code to support Fedora images.
//    It's mostly a copy of the ubuntu.go code.  As such, it's not very DRY, and
//    improvements could be acheived with a more considered approach. Perhaps this
//    is already in the works? 
//
// 2. I only started with golang yesterday. Sorry if my work is ugly.  
//
// 3. Since Fedora 15, systemd has replaced sysvinit by default. At the time of 
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
// 4. By default, Fedora does not enable IP packet forwarding, which is needed
//    for containers to be routable to/from the outside world. To enable forwarding,
//    one must execute:
//       sudo sysctl -w net.ipv4.ip_forward=1
//
//    To make this change persistent between reboots, one should execute:
//       sudo sh -c 'echo net.ipv4.ip_forward = 1 >> /etc/sysctl.d/80-docker.conf'
//
//    The purpose of the FedoraProvisioner.EnableIpForwarding() method is to
//    perform both commands.
//
//    TODO: Can we do the same thing with the --ip-forward=true option? Is that persistent?
//
// 
// 5. Fedora has the docker-io package available.  Therefore, one may make use of 
//    that, by including docker in the package list.  One can then skip the 
//    installDockerGeneric() step in FedoraProvisioner.Provision(...) (TBC)
//
//
// 6. In Fedora, the systemd service script for docker is located at /usr/lib/systemd/system
//    It sources the following three files:
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
//    (Note, this is not consistent with the seperation of concerns that Fedora 
//    appears to be attempting - kind of a shame to barf all over someone's nice
//    clean work :o(  )
//
//    None of the environment variables used by the fedora service script match the 
//    the DOCKER_OPTS written out by GenericProvisioner.GenerateDockerOptions().  
//    
//    We could make the environment variable name a configurable parameter.  
//    However, even if we used the OPTIONS variable for Fedora, then any existing
//    OPTIONS configuration would be overwritten.  The template would have to be 
//    modified to do `OPTIONS="$OPTIONS <new config>"`
// 
//    Rather than modify existing code, the MakeConfigOptionsCompatible()
//    method is provided in this Fedora provisioner.  It's purpose is to blend 
//    the DOCKER_OPTS variable into any existing OPTIONS variable, preserving both.
//
//    TODO: By default OPTIONS='--selinux-enabled'.  What happens if
//           we specify --selinux-enabled=false on the CLI?
//
//    TODO: Would be nice to have a configuration post-hook to tie into, to fire 
//    adaptation code.
//
// 7. AUFS isn't an option on Fedora/Redhat. I'm not even sure it's an option in 
//    recent versions of Ubuntu.  
// 
//    Fedora 21 shipped with the 3.16 kernel, which includes "overlayfs". However,
//    I can't get that to work with docker.  Meh.  
//
//    Regardless, the preferred storage driver on Fedora, which seems to work out 
//    of the box, is "devicemapper".  If no storage driver is set, then we use 
//    devicemapper by default. 
//
// 8. When using the devicemapper storage driver, and if no options are set, 
//    then docker will complain as follows: 
//
//      "ERRO[0000] WARNING: No --storage-opt dm.thinpooldev specified, using
//       loopback; this configuration is strongly discouraged for production use "
//
//     Not the end of the world, but we should use a default that is sensible 
//     for production. So, in FedoraProvisioner.Provision(), we check for this 
//     case, and use dm.thinpooldev as the default storage-driver-opt.  
//
//     To support this, modifications were made to the following files:
//     commands/commands.go        - the --storage-driver-opt flag was added
//     commands/create.go          - the --storage-driver-opt flag was added
//     libmachine/engine/engine.go - the StorageDriverOpt field was added
//     libmachine/provision/generic.go - the template was updated

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
	DockerSysctlFile    string
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
			OsReleaseId:       "fedora",
			Packages: []string{
				"curl",       
				"docker",
			},
			Driver: d,
		},
   		FedoraProvisionerExt{
			SystemdEnabled: true,
			DockerSysctlFile: "/etc/sysctl.d/80-docker.conf",
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
	command = fmt.Sprintf("sudo sh -c 'echo net.ipv4.ip_forward = 1 >> %s'", provisioner.DockerSysctlFile)
	if _, err := provisioner.SSHCommand(command); err != nil {
		log.Debug("Failed to run SSH command to make ip forwarding to docker containers permanent")
		return err
	}
	
	return nil
}

func (provisioner *FedoraProvisioner) MakeConfigOptionsCompatible() error {
		
	var command string
    log.Debug("About to attempt to fix options.")
	
	// Blend DOCKER_OPTS into the existing OPTIONS, for fedora compatability

    log.Debug("About to attempt to fix options.", command)

	command = fmt.Sprintf("sudo sh -c 'echo OPTIONS='$OPTIONS $DOCKER_OPTS' >> %s'", provisioner.DaemonOptionsFile)
	if _, err := provisioner.SSHCommand(command); err != nil {
		log.Debug("Failed to run SSH command to make config options compatible")
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

	// Sensible default StorageDriver
	// UAFS doesn't work on Fedora, but devicemapper does.
	if provisioner.EngineOptions.StorageDriver == "" {
		provisioner.EngineOptions.StorageDriver = "devicemapper"
	}

	// If using devicemapper, then sensible StorageDriverOpt
	if provisioner.EngineOptions.StorageDriver == "devicemapper" && 
	   provisioner.EngineOptions.StorageDriverOpt == "" {
		provisioner.EngineOptions.StorageDriver = "dm.thinpooldev"
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
	
	// Temporarily start docker, so dockerDaemonResponding test can work
	if err := provisioner.Service("docker", pkgaction.Start); err != nil {
		return err
	}
		
	if err := utils.WaitFor(provisioner.dockerDaemonResponding); err != nil {
		return err
	}

	if err := provisioner.Service("docker", pkgaction.Stop); err != nil {
		return err
	}
		
	if err := makeDockerOptionsDir(provisioner); err != nil {
		return err
	}

	provisioner.AuthOptions = setRemoteAuthOptions(provisioner)

	if err := ConfigureAuth(provisioner); err != nil {
		return err
	}

	// Make the general config compatible with Fedora
    if err := provisioner.MakeConfigOptionsCompatible(); err != nil {
		return err
	}

	if err := configureSwarm(provisioner, swarmOptions); err != nil {
		return err
	}

	return nil
}
