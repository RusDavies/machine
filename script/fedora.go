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

func init() {
	Register("Fedora", &RegisteredProvisioner{
		New: NewFedoraProvisioner,
	})
}

func NewFedoraProvisioner(d drivers.Driver) Provisioner {
	return &FedoraProvisioner{
		GenericProvisioner{
			DockerOptionsDir:  "/etc/docker",
			DaemonOptionsFile: "/etc/default/docker",
			OsReleaseId:       "fedora",
			Packages: []string{
				"curl",       
				"docker",
			},
			Driver: d,
			SystemdEnabled: true,
		},
	}
}

type FedoraProvisioner struct {
	GenericProvisioner
}

func (provisioner *FedoraProvisioner) SystemdCheck() error {
	// Fedora 15 onwards replaced sysvinit w/ systemd.  As of Fedora 21, 
	// the /sbin/service command still proxies to /bin/systemctl. However, 
	// this may change in the future, so let's avoid that foreseeable issue. 

	// This command detects whether /sbin/init is running.  If it is, then the
	// host is using sysvinit; otherwise, it's using systemd
	var (
		command 	string
		reader		bytes.Buffer
	)

	command = "pidof /sbin/init &>/dev/null && echo sysvinit || echo systemd" 

	response, err := provisioner.SSHCommand(command)
	if err != nil {
		return err
	}
	
	if _, err := reader.ReadFrom(response.Stdout); err != nil {
		return err
	}
	
	result := reader.String()
	
	fmt.Sprintf("DEBUG: response from pidof: ", result)
	
	if result == "systemd" {
		provisioner.SystemdEnabled = true
	} else {
		provisioner.SystemdEnabled = false
	}
	
	return nil
}

func (provisioner *FedoraProvisioner) EnableIpForwarding() error {
	var command string
	command = "sysctl -w net.ipv4.ip_forward=1"
	
	if _, err := provisioner.SSHCommand(command); err != nil {
		log.Debug("Failed to run command to enable ip forwarding to docker containers")
		return err
	}
	
	command = "sudo sh -c 'echo net.ipv4.ip_forward = 1 >> /etc/sysctl.d/80-docker.conf'"
	if _, err := provisioner.SSHCommand(command); err != nil {
		log.Debug("Failed to run command to make ip forwarding to docker containers permanent")
		return err
	}

}

func (provisioner *FedoraProvisioner) Service(name string, action pkgaction.ServiceAction) error {
	var command string; 
	log.Debugf("DEBUG: Service() - called")

	// The command varies depending on whether the host is using sysvinit or systemd
	if provisioner.SystemdEnabled {
		command = fmt.Sprintf("sudo systemctl %s %s", action.String(), name)  // systemd method
	} else {
		command = fmt.Sprintf("sudo service %s %s", name, action.String()) // SysVinit method
	}	

	log.Debugf(fmt.Sprintf("DEBUG: Service() - about to run: %s", command) )
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
		packageAction = "update" // TODO: Should this be update or upgrade? What's the intended effect?
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
	log.Debugf("DEBUG: Using the FedoraProvisioner")
	provisioner.SwarmOptions = swarmOptions
	provisioner.AuthOptions = authOptions
	provisioner.EngineOptions = engineOptions

	if provisioner.EngineOptions.StorageDriver == "" {
		provisioner.EngineOptions.StorageDriver = "aufs"
	}

	log.Debugf("DEBUG: Provision() - About to call provisioner.Driver.SetHostname()")
	if err := provisioner.SetHostname(provisioner.Driver.GetMachineName()); err != nil {
		return err
	}
	log.Debugf("DEBUG: Provision() - Returned from provisioner.Driver.SetHostname()")

	log.Debugf("DEBUG: Provision() - About to call provisioner.EnableIpForwarding()")
	if err := provisioner.SetHostname(provisioner.EnableIpForwarding()); err != nil {
		return err
	}
	log.Debugf("DEBUG: Provision() - Returned from provisioner.EnableIpForwarding()")

	log.Debugf("DEBUG: Provision() - About to call provisioner.SystemdCheck()")
	if err := provisioner.SystemdCheck(); err != nil {
		return err
	}
	log.Debugf("DEBUG: Provision() - Returned from provisioner.SystemdCheck()")

	for _, pkg := range provisioner.Packages {
    	log.Debugf("DEBUG: Provision() - About to call provisioner.Package(...)")
		if err := provisioner.Package(pkg, pkgaction.Install); err != nil {
			return err
		}
    	log.Debugf("DEBUG: Provision() - Returned from provisioner.Package(...)")
	}

	log.Debugf("DEBUG: Provision() - About to run installDockerGeneric(...) ")
	if err := installDockerGeneric(provisioner); err != nil {
		return err
	}
	log.Debugf("DEBUG: Provision() - Returned from installDockerGeneric(...) ")

	if err := utils.WaitFor(provisioner.dockerDaemonResponding); err != nil {
		return err
	}

	log.Debugf("DEBUG: Provision() - Abut to create the Docker options directory ")
	if err := makeDockerOptionsDir(provisioner); err != nil {
		return err
	}

	log.Debugf("DEBUG: Provision() - About to call remoteAuthOptions(...) ")
	provisioner.AuthOptions = setRemoteAuthOptions(provisioner)

	log.Debugf("DEBUG: Provision() - About to call ConfigureAuth(...) ")
	if err := ConfigureAuth(provisioner); err != nil {
		return err
	}

	log.Debugf("DEBUG: Provision() - About to call configureSwarm(...) ")
	if err := configureSwarm(provisioner, swarmOptions); err != nil {
		return err
	}

	return nil
}
